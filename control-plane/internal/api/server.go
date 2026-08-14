package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"octoport/control-plane/internal/auth"
	"octoport/control-plane/internal/cache"
	"octoport/control-plane/internal/config"
	"octoport/control-plane/internal/db"
	"octoport/control-plane/internal/events"
	"octoport/control-plane/internal/tunnel"
)

// Server wires every HTTP dependency together and routes requests.
type Server struct {
	Cfg   *config.Config
	DB    *db.DB
	Auth  *auth.Manager
	Cache *cache.Client
	Hub   *tunnel.Hub
	Log   *slog.Logger
	// Events is the per-user pub/sub broker backing the SSE stream. Tunnel
	// lifecycle changes are published here so connected GUIs update instantly.
	Events *events.Broker

	// cliMu guards the in-memory browser-login sessions. A session is created
	// when `octoport login` starts, completed by an email/password or GitHub sign
	// in on the /auth/cli/login page, and consumed by the CLI's poll loop.
	cliMu       sync.Mutex
	cliSessions map[string]*cliSession
}

// New constructs a Server.
func New(cfg *config.Config, d *db.DB, am *auth.Manager, c *cache.Client, h *tunnel.Hub, log *slog.Logger) *Server {
	s := &Server{Cfg: cfg, DB: d, Auth: am, Cache: c, Hub: h, Log: log,
		cliSessions: make(map[string]*cliSession), Events: events.New()}
	// Every hub routing change re-broadcasts the user's full tunnel list so
	// SSE subscribers stay in sync without polling.
	h.OnTunnelChange = s.publishUserList
	return s
}

// Routes returns the full API mux with middleware applied.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Auth
	mux.HandleFunc("POST /api/v1/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)

	// GitHub OAuth sign-in (public: begin + exchange; callback hits the
	// redirect URI the GitHub App is registered with)
	mux.HandleFunc("POST /api/v1/auth/github/begin", s.handleGitHubBegin)
	mux.HandleFunc("POST /api/v1/auth/github/exchange", s.handleGitHubExchange)
	mux.HandleFunc("GET /auth/github/callback", s.handleGitHubCallback)

	// CLI browser login: the terminal starts a session, the browser signs in
	// on /auth/cli/login, and the CLI polls for the token.
	mux.HandleFunc("POST /api/v1/auth/cli/session", s.handleCLISessionCreate)
	mux.HandleFunc("POST /api/v1/auth/cli/complete", s.handleCLIComplete)
	mux.HandleFunc("POST /api/v1/auth/cli/token", s.handleCLIPoll)
	mux.HandleFunc("GET /auth/cli/login", s.handleCLILoginPage)
	mux.HandleFunc("GET /auth/cli/done", s.handleCLIDonePage)

	// Authenticated API
	mux.Handle("GET /api/v1/me", s.authed("api", http.HandlerFunc(s.handleMe)))
	mux.Handle("POST /api/v1/auth/refresh", s.authed("api", http.HandlerFunc(s.handleRefresh)))
	mux.Handle("POST /api/v1/auth/agent-token", s.authed("api", http.HandlerFunc(s.handleAgentToken)))
	mux.Handle("POST /api/v1/tunnels", s.authed("api", http.HandlerFunc(s.handleCreateTunnel)))
	mux.Handle("GET /api/v1/tunnels", s.authed("api", http.HandlerFunc(s.handleListTunnels)))
	mux.Handle("DELETE /api/v1/tunnels/{id}", s.authed("api", http.HandlerFunc(s.handleDeleteTunnel)))
	mux.Handle("PATCH /api/v1/tunnels/{id}", s.authed("api", http.HandlerFunc(s.handleSetTunnelEnabled)))
	mux.Handle("GET /api/v1/events", s.authed("api", http.HandlerFunc(s.handleListEvents)))
	mux.Handle("GET /api/v1/events/stream", s.authed("api", http.HandlerFunc(s.handleEventStream)))

	// Agent WebSocket endpoint (JWT with agent scope)
	mux.Handle("/agent/connect", http.HandlerFunc(s.handleAgentWS))

	return s.recoverPanic(s.rateLimit(s.requestLog(mux)))
}

// AgentMux exposes only the agent WebSocket ingress on its own listener, so
// agents never share the public REST surface.
func (s *Server) AgentMux() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/agent/connect", http.HandlerFunc(s.handleAgentWS))
	return s.recoverPanic(s.requestLog(mux))
}

// requestLog adds minimal structured request logging.
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.Log.Debug("http", "method", r.Method, "path", r.URL.Path,
			"remote", r.RemoteAddr, "dur", time.Since(start).String())
	})
}

// rateLimit is a sliding-window limiter keyed by client IP.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, allowed, err := s.Cache.TokenBucket(r.Context(), "rl:ip:"+clientIP(r), s.Cfg.RateLimitPerMinute, time.Minute)
		if err != nil {
			s.Log.Warn("rate limit check failed", "err", err)
			next.ServeHTTP(w, r)
			return
		}
		if !allowed {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoverPanic turns handler panics into 500s instead of dropping the conn.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				s.Log.Error("panic in handler", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return nil }
