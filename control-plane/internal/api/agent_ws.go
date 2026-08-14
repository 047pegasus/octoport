package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"octoport/control-plane/internal/cache"
	"octoport/control-plane/internal/db"
	"octoport/control-plane/internal/tunnel"
)

// handleAgentWS upgrades the socket, authenticates the agent JWT, claims any
// active tunnels for that user, and pumps frames until the connection dies.
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	token := s.bearerToken(r)
	if token == "" {
		writeErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	if token == "" {
		writeErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	claims, err := s.Auth.Parse(token)
	if err != nil || claims.Scope != "agent" {
		writeErr(w, http.StatusUnauthorized, "invalid agent token")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(int64(s.Cfg.MaxFrameSize + 9))

	agentID := "agent-" + claims.UserID + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	agent := tunnel.NewAgent(agentID, claims.UserID, conn, 1024)
	s.agentClaimTunnels(r, agent)
	s.Hub.RegisterAgent(agent, s.agentTunnelEntries(r, claims.UserID))

	go agent.WriteLoop(r.Context())
	agent.ReadLoop(r.Context(), s.Cfg.MaxFrameSize, func(a *tunnel.Agent) {
		s.Hub.AgentGone(r.Context(), s.Cache, s.DB, a)
		s.Log.Info("agent disconnected", "agent", a.ID, "user", a.UserID)
	})
}

// agentClaimTunnels binds this agent to the user's active tunnels: cache
// entries get the agent id and a fresh idle TTL. Tunnels are re-armed from the
// database row (the source of truth) so a reconnect resurrects them even if the
// cache entry idled out; only tunnels past their hard deadline are skipped.
// Paused tunnels are deliberately NOT re-claimed — they keep their subdomain
// reserved (the DB row stays active) but are never routed or marked bound until
// the user resumes them.
func (s *Server) agentClaimTunnels(r *http.Request, a *tunnel.Agent) {
	for _, t := range s.dbActiveTunnels(r, a.UserID) {
		if !t.Enabled {
			continue
		}
		entry := &cache.TunnelEntry{
			TunnelID:  t.ID,
			Subdomain: t.Subdomain,
			UserID:    t.UserID,
			Protocol:  t.Protocol,
			LocalAddr: t.LocalAddr,
			AgentID:   a.ID,
			ExpiresAt: t.ExpiresAt,
			Enabled:   t.Enabled,
		}
		if err := s.Cache.PutTunnel(r.Context(), entry, s.Cfg.TunnelIdleTimeout); err != nil {
			s.Log.Warn("agent claim: cache put failed", "subdomain", t.Subdomain, "err", err)
		}
	}
}

// agentTunnelEntries re-reads the cache so RegisterAgent gets the freshest
// entries (which now carry the agent id).
func (s *Server) agentTunnelEntries(r *http.Request, userID string) []*cache.TunnelEntry {
	subs, err := s.Cache.ListUserTunnels(r.Context(), userID)
	if err != nil {
		return nil
	}
	out := make([]*cache.TunnelEntry, 0, len(subs))
	for _, sub := range subs {
		if t, err := s.Cache.GetTunnel(r.Context(), sub); err == nil && t.AgentID != "" {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) dbActiveTunnels(r *http.Request, userID string) []*db.Tunnel {
	rows, err := s.DB.ActiveTunnelsForUser(r.Context(), userID, time.Now())
	if err != nil {
		s.Log.Warn("agent claim: db read failed", "err", err)
		return nil
	}
	out := make([]*db.Tunnel, 0, len(rows))
	for _, t := range rows {
		out = append(out, t)
	}
	return out
}
