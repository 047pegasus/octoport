package cache

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client is a thin, purpose-built wrapper around Valkey (a Redis-fork,
// wire-compatible, so go-redis speaks to it natively).
//
// Responsibilities:
//   - Hot tunnel registry. Every tunnel has a TTL equal to the idle timeout,
//     so links auto-expire when inactive. Activity "touches" the key, which
//     resets the clock (sliding expiry).
//   - LRU/LFU eviction is configured on the Valkey server side
//     (maxmemory-policy); we surface that intent through config.
//   - Sliding-window rate limiting for the public API.
type Client struct {
	rc      redis.UniversalClient
	idleTTL time.Duration
}

// Options mirrors the subset of config the cache layer cares about.
type Options struct {
	Addrs    []string
	TLS      bool
	TTL      time.Duration
	IdleTTL  time.Duration // tunnel idle window; sliding reset on activity
	DB       int
	Username string
	Password string
}

// New dials Valkey. It is resilient to the container still booting.
func New(ctx context.Context, o Options) (*Client, error) {
	var tlsConf *tls.Config
	if o.TLS {
		tlsConf = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	rc := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:     o.Addrs,
		TLSConfig: tlsConf,
		DB:        o.DB,
		Username:  o.Username,
		Password:  o.Password,
	})

	var err error
	for attempt := 1; attempt <= 10; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = rc.Ping(pingCtx).Err()
		cancel()
		if err == nil {
			break
		}
		if attempt == 10 {
			rc.Close()
			return nil, fmt.Errorf("connect to valkey: %w", err)
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return &Client{rc: rc, idleTTL: o.IdleTTL}, nil
}

// Close releases the connection pool.
func (c *Client) Close() error { return c.rc.Close() }

// TunnelEntry is what the proxy needs to route a public request to an agent.
type TunnelEntry struct {
	TunnelID  string    `json:"tunnel_id"`
	Subdomain string    `json:"subdomain"`
	UserID    string    `json:"user_id"`
	Protocol  string    `json:"protocol"`
	LocalAddr string    `json:"local_addr"`
	AgentID   string    `json:"agent_id"`
	ExpiresAt time.Time `json:"expires_at"` // hard deadline: never slides
	Enabled   bool      `json:"enabled"`    // false = paused; subdomain stays reserved
}

// PutTunnel registers a tunnel with a TTL and tracks it under the owner. The
// TTL is the idle window; it can never outlive the tunnel's hard deadline.
func (c *Client) PutTunnel(ctx context.Context, t *TunnelEntry, ttl time.Duration) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	effective := c.effectiveTTL(t.ExpiresAt, ttl)
	if effective <= 0 {
		return nil // already past the hard deadline
	}
	pipe := c.rc.Pipeline()
	pipe.Set(ctx, tunnelKey(t.Subdomain), raw, effective)
	pipe.Set(ctx, tunnelByIDKey(t.TunnelID), t.Subdomain, effective)
	pipe.SAdd(ctx, userTunnelsKey(t.UserID), t.Subdomain)
	pipe.Expire(ctx, userTunnelsKey(t.UserID), effective)
	_, err = pipe.Exec(ctx)
	return err
}

// GetTunnel looks up a tunnel by subdomain and slides its idle window forward
// on a hit — any activity resets the idle clock, but never past the tunnel's
// hard deadline (after which the key expires unconditionally).
func (c *Client) GetTunnel(ctx context.Context, subdomain string) (*TunnelEntry, error) {
	t, err := c.PeekTunnel(ctx, subdomain)
	if err != nil {
		return nil, err
	}
	// Sliding expiry: reset to the full idle window, capped at the deadline.
	if ttl := c.effectiveTTL(t.ExpiresAt, c.idleTTL); ttl > 0 {
		c.rc.Expire(ctx, tunnelKey(subdomain), ttl)
	}
	return t, nil
}

// PeekTunnel reads a tunnel without sliding its idle window. Used by API
// listing and agent claims, which must not count as tunnel activity.
func (c *Client) PeekTunnel(ctx context.Context, subdomain string) (*TunnelEntry, error) {
	raw, err := c.rc.Get(ctx, tunnelKey(subdomain)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrMiss
	}
	if err != nil {
		return nil, err
	}
	var t TunnelEntry
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// effectiveTTL returns how long a tunnel key should live: at most the idle
// window and never past the hard deadline. Zero/negative means "already dead".
func (c *Client) effectiveTTL(deadline time.Time, idle time.Duration) time.Duration {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}
	if idle <= 0 || remaining < idle {
		return remaining
	}
	return idle
}

// RemoveTunnel deletes a tunnel and its index entries.
func (c *Client) RemoveTunnel(ctx context.Context, t *TunnelEntry) error {
	pipe := c.rc.Pipeline()
	pipe.Del(ctx, tunnelKey(t.Subdomain))
	pipe.Del(ctx, tunnelByIDKey(t.TunnelID))
	pipe.SRem(ctx, userTunnelsKey(t.UserID), t.Subdomain)
	_, err := pipe.Exec(ctx)
	return err
}

// ListUserTunnels returns the subdomains currently live for a user.
func (c *Client) ListUserTunnels(ctx context.Context, userID string) ([]string, error) {
	return c.rc.SMembers(ctx, userTunnelsKey(userID)).Result()
}

// CountLiveTunnels counts how many of a user's tunnels still have a live key
// (i.e. have not been dissolved by idle expiry or the hard deadline).
func (c *Client) CountLiveTunnels(ctx context.Context, userID string) (int, error) {
	subs, err := c.ListUserTunnels(ctx, userID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sub := range subs {
		if exists, err := c.Exists(ctx, sub); err == nil && exists {
			n++
		}
	}
	return n, nil
}

// Touch resets the idle window for a tunnel without a full round-trip read,
// never extending past the hard deadline.
func (c *Client) Touch(ctx context.Context, subdomain string, ttl time.Duration) error {
	raw, err := c.rc.Get(ctx, tunnelKey(subdomain)).Bytes()
	if err != nil {
		return err
	}
	var t TunnelEntry
	if err := json.Unmarshal(raw, &t); err != nil {
		return err
	}
	if eff := c.effectiveTTL(t.ExpiresAt, ttl); eff > 0 {
		return c.rc.Expire(ctx, tunnelKey(subdomain), eff).Err()
	}
	return c.rc.Del(ctx, tunnelKey(subdomain)).Err()
}

// Exists reports whether a tunnel key is still live (not expired/evicted).
func (c *Client) Exists(ctx context.Context, subdomain string) (bool, error) {
	n, err := c.rc.Exists(ctx, tunnelKey(subdomain)).Result()
	return n > 0, err
}

// SubdomainByID resolves a tunnel id to its subdomain via the index key.
func (c *Client) SubdomainByID(ctx context.Context, id string) (string, error) {
	sub, err := c.rc.Get(ctx, tunnelByIDKey(id)).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrMiss
	}
	return sub, err
}

// TokenBucket is a fixed-window rate limiter backed by INCR + EXPIRE.
// It returns how many requests remain and whether the call is allowed.
func (c *Client) TokenBucket(ctx context.Context, key string, limit int, window time.Duration) (remaining int, allowed bool, err error) {
	n, err := c.rc.Incr(ctx, key).Result()
	if err != nil {
		return 0, false, err
	}
	if n == 1 {
		// first hit in this window: establish the TTL so it self-resets
		if err := c.rc.Expire(ctx, key, window).Err(); err != nil {
			return 0, false, err
		}
	}
	remaining = limit - int(n)
	if remaining < 0 {
		remaining = 0
	}
	return remaining, n <= int64(limit), nil
}

// SetJSON stores a JSON value under key with a TTL.
func (c *Client) SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.rc.Set(ctx, key, raw, ttl).Err()
}

// GetJSON reads a JSON value under key. Returns ErrMiss when absent/expired.
func (c *Client) GetJSON(ctx context.Context, key string, v any) error {
	raw, err := c.rc.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return ErrMiss
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// GetDelJSON reads and atomically deletes a JSON value under key. The bool
// reports whether the key existed. Used for single-use tokens.
func (c *Client) GetDelJSON(ctx context.Context, key string, v any) (bool, error) {
	raw, err := c.rc.GetDel(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, err
	}
	return true, nil
}

// Del removes a key if present.
func (c *Client) Del(ctx context.Context, key string) error {
	return c.rc.Del(ctx, key).Err()
}

// ErrMiss indicates a cache lookup found nothing (expired or never existed).
var ErrMiss = errors.New("cache: miss")

func tunnelKey(sub string) string       { return "tunnel:" + sub }
func tunnelByIDKey(id string) string    { return "tunnel:byid:" + id }
func userTunnelsKey(user string) string { return "user:tunnels:" + user }
