package api

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"octoport/control-plane/internal/cache"
	"octoport/control-plane/internal/db"

	"github.com/jackc/pgx/v5/pgconn"
)

type createTunnelReq struct {
	Protocol         string `json:"protocol"`         // http | tcp
	LocalAddr        string `json:"localAddr"`        // e.g. 127.0.0.1:3000
	Subdomain        string `json:"subdomain"`        // optional custom subdomain
	ExpiresInSeconds int64  `json:"expiresInSeconds"` // optional lifetime cap (0 = default max)
}

// handleCreateTunnel allocates a random public subdomain for the caller. The
// tunnel is written to both YugabyteDB (source of truth, sharded on user id)
// and Valkey (hot routing path with a sliding idle TTL). It becomes routable
// the moment the user's agent connects and claims it.
func (s *Server) handleCreateTunnel(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())

	var req createTunnelReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Protocol = strings.ToLower(req.Protocol)
	if req.Protocol != "http" && req.Protocol != "tcp" {
		writeErr(w, http.StatusBadRequest, "protocol must be 'http' or 'tcp'")
		return
	}
	if err := validateLocalAddr(req.LocalAddr); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid local_addr: "+err.Error())
		return
	}

	// Enforce the free-plan limit (live cache count — dissolved tunnels don't
	// hold quota).
	active, err := s.Cache.CountLiveTunnels(r.Context(), claims.UserID)
	if err != nil {
		s.Log.Error("create tunnel: count failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not count tunnels")
		return
	}
	if active >= s.Cfg.MaxTunnelsPerUser {
		writeErr(w, http.StatusTooManyRequests,
			"tunnel limit reached (free plan: "+itoa(s.Cfg.MaxTunnelsPerUser)+")")
		return
	}

	// Reject exposing the same local address+protocol twice: a user opening
	// two tunnels to the same port is almost always a mistake, and the second
	// one would just sit "awaiting agent" forever (the agent only binds one
	// agent per local addr). Custom subdomains are still allowed to differ.
	if dup, err := s.userHasLiveTunnel(r.Context(), claims.UserID, req.Protocol, req.LocalAddr); err != nil {
		s.Log.Error("create tunnel: duplicate check failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not check duplicates")
		return
	} else if dup {
		writeErr(w, http.StatusConflict,
			"you already have a live "+req.Protocol+" tunnel on "+req.LocalAddr)
		return
	}

	// Custom subdomain if requested, otherwise a random collision-safe one.
	sub := ""
	if req.Subdomain != "" {
		if !validSubdomain(req.Subdomain) {
			writeErr(w, http.StatusBadRequest,
				"subdomain must be 3-63 characters (a-z, 0-9, dashes)")
			return
		}
		if s.Cfg.IsReservedSubdomain(req.Subdomain) {
			writeErr(w, http.StatusConflict,
				"subdomain '"+req.Subdomain+"' is reserved for another service on this domain")
			return
		}
		if _, err := s.Cache.GetTunnel(r.Context(), req.Subdomain); err == nil {
			writeErr(w, http.StatusConflict, "subdomain already in use")
			return
		} else if !errors.Is(err, cache.ErrMiss) {
			writeErr(w, http.StatusInternalServerError, "could not check subdomain")
			return
		}
		sub = req.Subdomain
	} else {
		for i := 0; i < 10; i++ {
			candidate := randomLabel(s.Cfg.RandomDomainChars)
			if s.Cfg.IsReservedSubdomain(candidate) {
				continue
			}
			if _, err := s.Cache.GetTunnel(r.Context(), candidate); err == cache.ErrMiss {
				sub = candidate
				break
			}
		}
		if sub == "" {
			writeErr(w, http.StatusInternalServerError, "could not allocate subdomain")
			return
		}
	}

	// Hard deadline: the tunnel can never live longer than this, regardless of
	// activity. An optional per-tunnel cap lets users choose shorter lifetimes.
	// The idle window (shorter, sliding) is enforced by the cache TTL.
	deadline := time.Now().Add(s.Cfg.TunnelMaxLifetime)
	if req.ExpiresInSeconds > 0 {
		reqLifetime := time.Duration(req.ExpiresInSeconds) * time.Second
		if reqLifetime < s.Cfg.TunnelMaxLifetime {
			deadline = time.Now().Add(reqLifetime)
		}
	}
	tunnelID := randomUUID()

	t := &cache.TunnelEntry{
		TunnelID:  tunnelID,
		Subdomain: sub,
		UserID:    claims.UserID,
		Protocol:  req.Protocol,
		LocalAddr: req.LocalAddr,
		AgentID:   "", // claimed when an agent connects
		ExpiresAt: deadline,
		Enabled:   true,
	}

	if err := s.Cache.PutTunnel(r.Context(), t, s.Cfg.TunnelIdleTimeout); err != nil {
		s.Log.Error("create tunnel: cache put failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not persist tunnel")
		return
	}
	if err := s.DB.RecordTunnel(r.Context(), &db.Tunnel{
		ID:        tunnelID,
		UserID:    claims.UserID,
		Subdomain: sub,
		Protocol:  req.Protocol,
		LocalAddr: req.LocalAddr,
		Status:    "active",
		ExpiresAt: deadline,
	}); err != nil {
		s.Log.Error("create tunnel: db record failed", "err", err)
		// Roll back the cache entry so nothing leaks a slot this tunnel can
		// never use.
		_ = s.Cache.RemoveTunnel(r.Context(), t)
		// A unique constraint on subdomain means another user grabbed it between
		// our cache check and the DB write — surface that as a conflict.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeErr(w, http.StatusConflict, "subdomain already in use")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not persist tunnel")
		return
	}
	_ = s.DB.LogEvent(r.Context(), claims.UserID, "tunnel.create",
		map[string]any{"subdomain": sub, "protocol": req.Protocol})

	// If this user already has a live agent, bind the new tunnel to it right
	// away so it becomes routable immediately instead of waiting for the next
	// agent (re)connect. One agent connection serves every tunnel for a user.
	if agent, ok := s.Hub.AgentByUser(claims.UserID); ok {
		t.AgentID = agent.ID
		if err := s.Cache.PutTunnel(r.Context(), t, s.Cfg.TunnelIdleTimeout); err != nil {
			s.Log.Warn("create tunnel: rebind agent failed", "subdomain", sub, "err", err)
		} else {
			s.Hub.RegisterTunnel(agent, t)
		}
	}

	publicURL := s.Cfg.TunnelURL(sub)
	endpoint := ""
	switch req.Protocol {
	case "tcp":
		endpoint = s.Cfg.TCPURL(sub)
	}
	if endpoint != "" {
		publicURL = req.Protocol + "://" + endpoint
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":        tunnelID,
		"subdomain": sub,
		"url":       publicURL,
		"endpoint":  endpoint,
		"protocol":  req.Protocol,
		"localAddr": req.LocalAddr,
		"expiresAt": deadline,
	})
	s.publishUserList(claims.UserID)
}

// userHasLiveTunnel reports whether the user already has a live tunnel with the
// same protocol + local address. Compares only the port (host normalization) so
// `127.0.0.1:3000` and `localhost:3000` count as duplicates.
func (s *Server) userHasLiveTunnel(ctx context.Context, userID, protocol, localAddr string) (bool, error) {
	subs, err := s.Cache.ListUserTunnels(ctx, userID)
	if err != nil {
		return false, err
	}
	wantPort := portOf(localAddr)
	for _, sub := range subs {
		t, err := s.Cache.PeekTunnel(ctx, sub)
		if err != nil {
			continue // dissolved between list and peek
		}
		if t.Protocol != protocol {
			continue
		}
		if portOf(t.LocalAddr) == wantPort && wantPort != "" {
			return true, nil
		}
	}
	return false, nil
}

// portOf extracts just the port from a host:port address ("" when malformed).
func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

func (s *Server) handleListTunnels(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())

	// Source of truth is the database: a tunnel the user created stays listed
	// until it reaches its hard deadline, regardless of agent connections or
	// cache TTL. Live state (bound / stats) is merged in from cache and hub.
	out, err := s.tunnelList(r.Context(), claims.UserID)
	if err != nil {
		s.Log.Error("list tunnels: db read failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not list tunnels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tunnels": out})
}

func (s *Server) handleDeleteTunnel(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())
	id := r.PathValue("id")

	// Find the subdomain via cache index (fast path) or DB (fallback).
	subdomain, err := s.Cache.SubdomainByID(r.Context(), id)
	if err != nil {
		var t *db.Tunnel
		t, err = s.DB.GetTunnelByID(r.Context(), id)
		if err != nil || t == nil {
			writeErr(w, http.StatusNotFound, "tunnel not found")
			return
		}
		if t.UserID != claims.UserID {
			writeErr(w, http.StatusForbidden, "not your tunnel")
			return
		}
		subdomain = t.Subdomain
	}

	// The cache entry may have idled out even though the DB row is still
	// active, so fall back to the DB row for ownership and teardown.
	t, err := s.Cache.GetTunnel(r.Context(), subdomain)
	if err != nil {
		row, dbErr := s.DB.GetTunnelByID(r.Context(), id)
		if dbErr != nil || row == nil {
			writeErr(w, http.StatusNotFound, "tunnel not found or expired")
			return
		}
		if row.UserID != claims.UserID {
			writeErr(w, http.StatusForbidden, "not your tunnel")
			return
		}
		// No live entry to route through; just drop the DB row.
		_ = s.DB.DeactivateTunnel(r.Context(), row.ID)
		_ = s.DB.LogEvent(r.Context(), claims.UserID, "tunnel.delete",
			map[string]any{"subdomain": subdomain})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
		s.publishUserList(claims.UserID)
		return
	}
	if t.UserID != claims.UserID {
		writeErr(w, http.StatusForbidden, "not your tunnel")
		return
	}

	if hubT, ok := s.Hub.Lookup(subdomain); ok {
		s.Hub.Expire(r.Context(), s.Cache, s.DB, subdomain, hubT)
	} else {
		_ = s.Cache.RemoveTunnel(r.Context(), t)
	}
	_ = s.DB.DeactivateTunnel(r.Context(), t.TunnelID)
	_ = s.DB.LogEvent(r.Context(), claims.UserID, "tunnel.delete",
		map[string]any{"subdomain": subdomain})

	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	s.publishUserList(claims.UserID)
}

type setEnabledReq struct {
	Enabled bool `json:"enabled"`
}

// handleSetTunnelEnabled pauses or resumes a tunnel without releasing its
// subdomain. A paused tunnel stops routing traffic (the proxy refuses it) but
// the row stays active until the hard deadline or an explicit delete, so the
// subdomain remains reserved for the owner. Re-enabling restores routing.
func (s *Server) handleSetTunnelEnabled(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())
	id := r.PathValue("id")

	var req setEnabledReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}

	row, err := s.DB.GetTunnelByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "tunnel not found")
		return
	}
	if row.UserID != claims.UserID {
		writeErr(w, http.StatusForbidden, "not your tunnel")
		return
	}

	if err := s.DB.SetTunnelEnabled(r.Context(), id, req.Enabled); err != nil {
		s.Log.Error("set tunnel enabled: db update failed", "id", id, "err", err)
		writeErr(w, http.StatusInternalServerError, "could not update tunnel")
		return
	}

	// Keep the cache entry (so the subdomain stays reserved) and re-route as
	// requested. When pausing, unbind from the hub so no traffic is relayed;
	// when resuming, claim it for the user's live agent if one is connected.
	// A paused tunnel's cache key lives until the hard deadline (not the idle
	// window) so its subdomain is never handed to someone else.
	if ce, err := s.Cache.PeekTunnel(r.Context(), row.Subdomain); err == nil {
		ce.Enabled = req.Enabled
		if !req.Enabled {
			ce.AgentID = ""
			if hubT, ok := s.Hub.Lookup(row.Subdomain); ok {
				s.Hub.ExpireIdle(r.Context(), s.Cache, s.DB, row.Subdomain, hubT)
			}
			// Re-add the entry paused with a deadline TTL so the subdomain
			// stays reserved until the hard deadline or an explicit delete.
			pauseTTL := time.Until(row.ExpiresAt)
			if err := s.Cache.PutTunnel(r.Context(), ce, pauseTTL); err != nil {
				s.Log.Warn("pause tunnel: cache put failed", "sub", row.Subdomain, "err", err)
			}
		} else {
			if agent, ok := s.Hub.AgentByUser(claims.UserID); ok {
				ce.AgentID = agent.ID
				if err := s.Cache.PutTunnel(r.Context(), ce, s.Cfg.TunnelIdleTimeout); err != nil {
					s.Log.Warn("resume tunnel: cache put failed", "sub", row.Subdomain, "err", err)
				} else {
					s.Hub.RegisterTunnel(agent, ce)
				}
			} else if err := s.Cache.PutTunnel(r.Context(), ce, s.Cfg.TunnelIdleTimeout); err != nil {
				s.Log.Warn("resume tunnel: cache put failed", "sub", row.Subdomain, "err", err)
			}
		}
	} else {
		// No cache entry (idled out). Just update the row; the next agent
		// (re)connect will re-claim enabled tunnels.
		s.Log.Debug("set tunnel enabled: no cache entry", "sub", row.Subdomain)
	}

	_ = s.DB.LogEvent(r.Context(), claims.UserID, "tunnel.enabled",
		map[string]any{"subdomain": row.Subdomain, "enabled": req.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"enabled": req.Enabled})
	s.publishUserList(claims.UserID)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"agents":  s.Hub.AgentCount(),
		"tunnels": s.Hub.TunnelCount(),
		"time":    time.Now().UTC(),
	})
}

func validateLocalAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || port == "" {
		return errors.New("host:port required")
	}
	return nil
}

// validSubdomain matches the relaxed DNS-label rules we allow for custom
// subdomains: lowercase letters, digits and dashes, 3-63 characters.
func validSubdomain(sub string) bool {
	if len(sub) < 3 || len(sub) > 63 {
		return false
	}
	for _, r := range sub {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func randomLabel(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		idx, _ := rand.Int(rand.Reader, max)
		b[i] = alphabet[idx.Int64()]
	}
	return string(b)
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	const hex = "0123456789abcdef"
	var out [36]byte
	idx := 0
	for i, c := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out[idx] = '-'
			idx++
		}
		out[idx] = hex[c>>4]
		out[idx+1] = hex[c&0x0f]
		idx += 2
	}
	return string(out[:])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
