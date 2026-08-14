package tunnel

import (
	"context"
	"log/slog"
	"time"

	"octoport/control-plane/internal/cache"
	"octoport/control-plane/internal/db"
)

// Sweeper expires links that sat idle longer than the configured window.
//
// The source of truth is the Valkey TTL: every request slides the key's
// lifetime forward, so a tunnel whose key has expired has been idle for
// > idleTimeout. On expiry we:
//
//  1. CLOSE every live stream so the agent tears down its local connection
//  2. drop the tunnel from the in-memory hub and the routing cache
//
// The database row is deliberately left active: the tunnel stays listed in
// the app until its hard deadline and is re-claimed by any agent that
// (re)connects. Only the hard deadline (ExpireStale) or an explicit delete
// marks a row inactive.
func (h *Hub) Sweep(ctx context.Context, c *cache.Client, d *db.DB, idleTimeout time.Duration) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.sweepOnce(ctx, c, d, idleTimeout)
		}
	}
}

func (h *Hub) sweepOnce(ctx context.Context, c *cache.Client, d *db.DB, idleTimeout time.Duration) {
	// Bulk-expire stale rows in the database (quota hygiene), then push a UI
	// refresh to any affected user so hard-deadline tunnels disappear live.
	if users, err := d.ExpireStale(ctx); err == nil {
		for _, uid := range users {
			h.notify(uid)
		}
	} else if err != nil {
		slog.Error("sweep: expire-stale failed", "err", err)
	}

	for subdomain, t := range h.Snapshot() {
		exists, err := c.Exists(ctx, subdomain)
		if err != nil {
			slog.Error("sweep: cache check failed", "subdomain", subdomain, "err", err)
			continue
		}
		if exists {
			continue
		}

		slog.Info("expiring idle tunnel", "subdomain", subdomain,
			"idle", idleTimeout.String())
		h.ExpireIdle(ctx, c, d, subdomain, t)
	}
}

// ExpireIdle tears down routing for an idle tunnel without destroying it:
// streams are closed, the hub entry and cache keys are dropped, but the
// database row stays active so the tunnel keeps appearing in the app and is
// re-claimed when an agent (re)connects.
func (h *Hub) ExpireIdle(ctx context.Context, c *cache.Client, d *db.DB, subdomain string, t *Tunnel) {
	for _, s := range t.Streams {
		if s.agent != nil {
			_ = s.SendClose()
		}
		s.Finish()
	}
	if t.Entry != nil {
		h.mu.Lock()
		delete(h.tunnels, subdomain)
		if a := h.agents[t.Entry.AgentID]; a != nil {
			delete(h.byAgent[t.Entry.AgentID], subdomain)
		}
		if set := h.byUser[t.Entry.UserID]; set != nil {
			delete(set, subdomain)
		}
		h.mu.Unlock()

		_ = c.RemoveTunnel(ctx, t.Entry)
		_ = d.LogEvent(ctx, t.Entry.UserID, "tunnel.idle",
			map[string]any{"subdomain": subdomain, "protocol": t.Entry.Protocol})
		h.notify(t.Entry.UserID)
	}
}

// Expire actively tears down a tunnel (used by the sweeper and on delete).
func (h *Hub) Expire(ctx context.Context, c *cache.Client, d *db.DB, subdomain string, t *Tunnel) {
	for _, s := range t.Streams {
		if s.agent != nil {
			_ = s.SendClose()
		}
		s.Finish()
	}
	if t.Entry != nil {
		h.mu.Lock()
		delete(h.tunnels, subdomain)
		if a := h.agents[t.Entry.AgentID]; a != nil {
			delete(h.byAgent[t.Entry.AgentID], subdomain)
		}
		if set := h.byUser[t.Entry.UserID]; set != nil {
			delete(set, subdomain)
		}
		h.mu.Unlock()

		_ = c.RemoveTunnel(ctx, t.Entry)
		_ = d.DeactivateTunnel(ctx, t.Entry.TunnelID)
		_ = d.LogEvent(ctx, t.Entry.UserID, "tunnel.expire",
			map[string]any{"subdomain": subdomain, "protocol": t.Entry.Protocol})
		h.notify(t.Entry.UserID)
	}
}

// AgentGone handles an agent disconnect. Tunnels the agent was serving are
// unbound (removed from the routing hub and their cache entries re-claimed for
// the next agent), but they are NOT destroyed: the database row stays active
// until its hard deadline, so the tunnel keeps appearing in the app and is
// immediately re-claimable when any agent (re)connects.
func (h *Hub) AgentGone(ctx context.Context, c *cache.Client, d *db.DB, a *Agent) {
	h.mu.Lock()
	subs := make([]string, 0, len(h.byAgent[a.ID]))
	for sub := range h.byAgent[a.ID] {
		subs = append(subs, sub)
	}
	h.mu.Unlock()

	for _, sub := range subs {
		// Close any in-flight streams so the agent tears down its local
		// connections, then drop the tunnel from the routing hub.
		if t, ok := h.Lookup(sub); ok {
			for _, s := range t.Streams {
				if s.agent != nil {
					_ = s.SendClose()
				}
				s.Finish()
			}
		}
		// Re-arm the cache entry without an agent so the next connection
		// claims it again. The DB row is left active.
		if ce, err := c.PeekTunnel(ctx, sub); err == nil {
			ce.AgentID = ""
			_ = c.PutTunnel(ctx, ce, 0)
		}
		_ = d.LogEvent(ctx, a.UserID, "tunnel.unbound",
			map[string]any{"subdomain": sub})
	}
	h.UnregisterAgent(a)
}
