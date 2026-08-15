package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SSE framing constants. The client keeps one long-lived connection instead of
// polling; every push is a server-originated event.
const (
	// statsInterval is the default cadence for live request/byte counters.
	// Overridable with OCTOPORT_STATS_INTERVAL: on a high-latency or metered
	// link, raising this to 2-5s cuts the steady-state SSE chatter
	// proportionally. The GUI's chart window is expressed in samples, so a
	// slower cadence simply widens the time span each chart covers.
	statsInterval    = time.Second
	heartbeatInterval = 15 * time.Second
)

// handleEventStream upgrades to a Server-Sent Events stream for the caller:
//
//  1. a "snapshot" frame with the full tunnel list on connect
//  2. "list" frames whenever the user's tunnels change (created, deleted,
//     bound, unbound, idled) — published on the broker by every mutation point
//  3. "stats" frames every statsInterval with in-memory live counters
//  4. a keep-alive comment so intermediate proxies don't reap the idle conn
//
// The stats frames come from the global broadcaster goroutine (RunStatsBroadcast)
// so the cost is one hub scan per interval, shared across all subscribers.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())
	userID := claims.UserID

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// write marshals payload as one SSE data frame and flushes it. JSON never
	// contains a raw newline, so this is safe to send as a single line.
	write := func(payload []byte) bool {
		_, err := fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		return err == nil
	}

	// Initial snapshot: what the user has now (DB-backed, same shape as
	// GET /api/v1/tunnels).
	if list, err := s.tunnelList(r.Context(), userID); err == nil {
		if b, jerr := json.Marshal(map[string]any{"type": "snapshot", "tunnels": list}); jerr == nil {
			if !write(b) {
				return
			}
		}
	}

	ch, cancel := s.Events.Subscribe(userID)
	defer cancel()

	hb := time.NewTicker(heartbeatInterval)
	defer hb.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-ch:
			if !write(payload) {
				return
			}
		case <-hb.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// tunnelList builds the DB-backed tunnel list a client sees, merging live
// routing state (bound flag) and hub stats. Shared by the REST list
// endpoint and the SSE snapshot.
func (s *Server) tunnelList(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.DB.ActiveTunnelsForUser(ctx, userID, time.Now())
	if err != nil {
		return nil, err
	}

	out := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		item := map[string]any{
			"id":        t.ID,
			"subdomain": t.Subdomain,
			"url":       s.Cfg.TunnelURL(t.Subdomain),
			"endpoint":  "",
			"protocol":  t.Protocol,
			"localAddr": t.LocalAddr,
			"bound":     false,
			"enabled":   t.Enabled,
			"expiresAt": t.ExpiresAt,
		}
		if ce, err := s.Cache.PeekTunnel(ctx, t.Subdomain); err == nil {
			item["bound"] = ce.AgentID != ""
			if hubT, ok := s.Hub.Lookup(t.Subdomain); ok {
				reqs, bin, bout, last := hubT.SnapshotStats()
				item["requests"] = reqs
				item["bytesIn"] = bin
				item["bytesOut"] = bout
				item["lastActiveAt"] = last
			}
		}
		switch t.Protocol {
		case "tcp":
			item["endpoint"] = s.Cfg.TCPURL(t.Subdomain)
			item["url"] = "tcp://" + s.Cfg.TCPURL(t.Subdomain)
		}
		out = append(out, item)
	}
	return out, nil
}

// tunnelStats snapshots one user's live in-memory counters (no DB access).
// It is called by the broadcaster goroutine, so its cost is O(user's tunnels).
func (s *Server) tunnelStats(userID string) []map[string]any {
	out := make([]map[string]any, 0, len(s.Hub.SnapshotByUser(userID)))
	for sub, t := range s.Hub.SnapshotByUser(userID) {
		reqs, bin, bout, last := t.SnapshotStats()
		out = append(out, map[string]any{
			"subdomain":    sub,
			"requests":     reqs,
			"bytesIn":      bin,
			"bytesOut":     bout,
			"lastActiveAt": last,
		})
	}
	return out
}

// publishUserList rebuilds a user's full list and broadcasts it to every live
// SSE subscriber. Wired as the Hub's OnTunnelChange hook and called directly
// from create/delete handlers.
func (s *Server) publishUserList(userID string) {
	if userID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	list, err := s.tunnelList(ctx, userID)
	if err != nil {
		s.Log.Warn("publish list: build failed", "user", userID, "err", err)
		return
	}
	if b, err := json.Marshal(map[string]any{"type": "list", "tunnels": list}); err == nil {
		s.Events.Publish(userID, b)
	}
}

// RunStatsBroadcast pushes live stats to every connected SSE subscriber on a
// shared ticker. One hub scan per interval serves all clients, keeping the
// cost flat regardless of how many GUIs are watching.
func (s *Server) RunStatsBroadcast(ctx context.Context) {
	interval := statsInterval
	if s.Cfg != nil && s.Cfg.StatsInterval > 0 {
		interval = s.Cfg.StatsInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			byUser := map[string][]map[string]any{}
			for _, t := range s.Hub.Snapshot() {
				if t.Entry == nil {
					continue
				}
				// Skip users with no live SSE subscriber. Building and
				// marshalling stats for an offline user is pure waste, and it
				// scales with the number of tunnels ever created rather than
				// the number of GUIs actually watching.
				if !s.Events.HasSubscribers(t.Entry.UserID) {
					continue
				}
				reqs, bin, bout, last := t.SnapshotStats()
				byUser[t.Entry.UserID] = append(byUser[t.Entry.UserID], map[string]any{
					"subdomain":    t.Entry.Subdomain,
					"requests":     reqs,
					"bytesIn":      bin,
					"bytesOut":     bout,
					"lastActiveAt": last,
				})
			}
			for userID, items := range byUser {
				b, err := json.Marshal(map[string]any{"type": "stats", "tunnels": items})
				if err != nil {
					continue
				}
				s.Events.Publish(userID, b)
			}
		}
	}
}
