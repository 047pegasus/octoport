package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every runtime knob for the control plane. Every field is
// sourced from the environment (optionally layered on top of a .env file),
// so no recompilation is ever needed to change behaviour.
type Config struct {
	// Environment / logging
	Environment string // dev | production
	LogLevel    string // debug | info | warn | error
	BaseDomain  string // suffix appended to every public URL, e.g. octoport.dev
	PublicBase  string // full public base URL incl scheme+host, e.g. https://octoport.dev (empty => derive from BaseDomain)
	TCPBase     string // domain the raw TCP proxy is published under, e.g. tcp.octoport.dev (empty => BaseDomain)
	TCPPort     int    // public TCP port clients dial for raw TCP tunnels (e.g. 443 via a TCP tunnel, 4443 direct)
	TCPTLS      bool   // terminate TLS on the public TCP proxy (encrypts all TCP tunnel traffic)
	TCPTLSCert  string // path to the wildcard cert for *.<TCPBaseDomain> (with TCPTLS)
	TCPTLSKey   string // path to the matching private key (with TCPTLS)

	// ReservedSubdomains are labels that may never be allocated as tunnel
	// subdomains. They belong to other services on the same base domain (e.g.
	// portainer.itanishq.space, the control plane's own octoport-control-plane),
	// so handing one out would both shadow the real app and yield a URL that
	// Traefik routes somewhere else. Matched case-insensitively.
	ReservedSubdomains []string

	// Public entry points
	APIAddr       string // REST API listener
	AgentWSAddr   string // Agent websocket listener
	PublicAddr    string // public HTTP(S) proxy listener
	PublicTCPAddr string // public raw TCP proxy listener (SNI-routed)
	PublicTLS     bool   // serve the public proxy with TLS (auto-cert)
	PublicTLSDir  string // dir to cache ACME certs when TLS is on

	// External systems
	DatabaseURL string // YugabyteDB / Postgres DSN (YugabyteDB is wire-compatible)
	DBPoolMax   int
	DBShards    int // number of hash tablets/shards for tunnel table

	ValkeyAddrs  []string // comma-separated list of Valkey nodes
	ValkeyTLS    bool
	ValkeyTTL    time.Duration // default cache TTL
	ValkeyPolicy string        // maxmemory-policy hint (informational)

	// Auth / security
	JWTSecret          string
	JWTTTL             time.Duration
	MaxTunnelsPerUser  int
	TunnelIdleTimeout  time.Duration // links dissolve after this inactivity window
	TunnelMaxLifetime  time.Duration // hard cap on a tunnel's life regardless of activity
	RandomDomainChars  int           // length of the random subdomain label
	PasswordMinLength  int
	RateLimitPerMinute int
	AgentAuthHeader    string // header agents present their JWT under

	// GitHub OAuth (optional sign-in)
	GitHubClientID     string        // OAuth App client id (enables GitHub sign-in when set)
	GitHubClientSecret string        // OAuth App client secret
	GitHubOAuthBase    string        // base URL for the GitHub authorize/access-token endpoints
	GitHubAPIBase      string        // base URL for the GitHub REST API (user info)
	OAuthRedirectBase  string        // public base URL where /auth/github/callback is reachable
	OAuthAttemptTTL    time.Duration // how long a pending sign-in attempt stays valid
	OAuthExchangeTTL   time.Duration // how long the app-side exchange code stays usable

	// Protocol
	MaxFrameSize       int
	MaxStreamsPerAgent int
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	ProxyTimeout       time.Duration // upstream dial timeout on the public proxy
	StatsInterval      time.Duration // cadence of live stats frames pushed over SSE
}

// Load builds a Config from the environment, applying defaults for anything
// missing so the server boots out-of-the-box.
func Load() (*Config, error) {
	c := &Config{
		Environment: get("OCTOPORT_ENV", "development"),
		LogLevel:    get("OCTOPORT_LOG_LEVEL", "info"),
		BaseDomain:  get("OCTOPORT_BASE_DOMAIN", "octoport.dev"),
		PublicBase:  get("OCTOPORT_PUBLIC_BASE", ""),
		TCPBase:     get("OCTOPORT_TCP_BASE", ""),
		TCPPort:     getInt("OCTOPORT_TCP_PORT", 443),

		ReservedSubdomains: splitCSV(get("OCTOPORT_RESERVED_SUBDOMAINS",
			"www,octoport,octoport-control-plane,portainer,grafana,traefik,checkmate,heartbeat,prometheus,loki,mail,blog,status")),

		APIAddr:       get("OCTOPORT_API_ADDR", ":8080"),
		AgentWSAddr:   get("OCTOPORT_AGENT_WS_ADDR", ":8081"),
	PublicAddr:    get("OCTOPORT_PUBLIC_ADDR", ":8090"),
		PublicTCPAddr: get("OCTOPORT_PUBLIC_TCP_ADDR", ":8091"),
		PublicTLS:     getBool("OCTOPORT_PUBLIC_TLS", false),
		PublicTLSDir:  get("OCTOPORT_PUBLIC_TLS_DIR", "./certs"),
		TCPTLS:        getBool("OCTOPORT_TCP_TLS", false),
		TCPTLSCert:    get("OCTOPORT_TCP_TLS_CERT", ""),
		TCPTLSKey:     get("OCTOPORT_TCP_TLS_KEY", ""),

		DatabaseURL: get("OCTOPORT_DATABASE_URL", "postgres://octoport:octoport@localhost:5433/octoport?sslmode=disable"),
		DBPoolMax:   getInt("OCTOPORT_DB_POOL_MAX", 32),
		DBShards:    getInt("OCTOPORT_DB_SHARDS", 8),

		ValkeyAddrs:  splitCSV(get("OCTOPORT_VALKEY_ADDRS", "localhost:6379")),
		ValkeyTLS:    getBool("OCTOPORT_VALKEY_TLS", false),
		ValkeyTTL:    getDuration("OCTOPORT_VALKEY_TTL", 10*time.Minute),
		ValkeyPolicy: get("OCTOPORT_VALKEY_EVICTION_POLICY", "allkeys-lru"),

		JWTSecret:          get("OCTOPORT_JWT_SECRET", "dev-secret-change-me"),
		JWTTTL:             getDuration("OCTOPORT_JWT_TTL", 24*time.Hour),
		MaxTunnelsPerUser:  getInt("OCTOPORT_MAX_TUNNELS_PER_USER", 5),
		TunnelIdleTimeout:  getDuration("OCTOPORT_TUNNEL_IDLE_TIMEOUT", 5*time.Minute),
		TunnelMaxLifetime:  getDuration("OCTOPORT_TUNNEL_MAX_LIFETIME", 36*time.Hour),
		RandomDomainChars:  getInt("OCTOPORT_RANDOM_DOMAIN_CHARS", 8),
		PasswordMinLength:  getInt("OCTOPORT_PASSWORD_MIN_LENGTH", 8),
		RateLimitPerMinute: getInt("OCTOPORT_RATE_LIMIT_PER_MINUTE", 120),
		AgentAuthHeader:    get("OCTOPORT_AGENT_AUTH_HEADER", "Authorization"),

		GitHubClientID:     get("OCTOPORT_GITHUB_CLIENT_ID", ""),
		GitHubClientSecret: get("OCTOPORT_GITHUB_CLIENT_SECRET", ""),
		GitHubOAuthBase:    get("OCTOPORT_GITHUB_OAUTH_BASE", "https://github.com"),
		GitHubAPIBase:      get("OCTOPORT_GITHUB_API_BASE", "https://api.github.com"),
		OAuthRedirectBase:  get("OCTOPORT_OAUTH_REDIRECT_BASE", ""),
		OAuthAttemptTTL:    getDuration("OCTOPORT_OAUTH_ATTEMPT_TTL", 10*time.Minute),
		OAuthExchangeTTL:   getDuration("OCTOPORT_OAUTH_EXCHANGE_TTL", 5*time.Minute),

		MaxFrameSize:       getInt("OCTOPORT_MAX_FRAME_SIZE", 1<<20),
		MaxStreamsPerAgent: getInt("OCTOPORT_MAX_STREAMS_PER_AGENT", 64),
		ReadTimeout:        getDuration("OCTOPORT_READ_TIMEOUT", 30*time.Second),
		WriteTimeout:       getDuration("OCTOPORT_WRITE_TIMEOUT", 30*time.Second),
		ProxyTimeout:       getDuration("OCTOPORT_PROXY_TIMEOUT", 30*time.Second),
		StatsInterval:      getDuration("OCTOPORT_STATS_INTERVAL", time.Second),
	}

	if c.JWTSecret == "dev-secret-change-me" && c.Environment == "production" {
		return nil, fmt.Errorf("OCTOPORT_JWT_SECRET must be set in production")
	}
	return c, nil
}

// TunnelURL builds the public URL a tunnel at `subdomain` is served at.
// OCTOPORT_PUBLIC_BASE (e.g. http://localhost:8090 locally, https://octoport.dev
// in production) is authoritative when set; otherwise it is derived from
// OCTOPORT_BASE_DOMAIN plus the TLS flag.
func (c *Config) TunnelURL(subdomain string) string {
	if c.PublicBase != "" {
		if u, err := url.Parse(strings.TrimRight(c.PublicBase, "/")); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + subdomain + "." + u.Host
		}
	}
	scheme := "http"
	if c.PublicTLS {
		scheme = "https"
	}
	return scheme + "://" + subdomain + "." + c.BaseDomain
}

// IsReservedSubdomain reports whether a subdomain label must never be handed
// out as a tunnel (it belongs to another service on the base domain). The
// comparison is case-insensitive so "Portainer" and "portainer" both match.
func (c *Config) IsReservedSubdomain(sub string) bool {
	sub = strings.ToLower(strings.TrimSpace(sub))
	for _, r := range c.ReservedSubdomains {
		if strings.ToLower(strings.TrimSpace(r)) == sub {
			return true
		}
	}
	return false
}

// TCPBaseDomain returns the domain raw TCP tunnels are served under,
// defaulting to the main base domain when not configured.
func (c *Config) TCPBaseDomain() string {
	if c.TCPBase != "" {
		return strings.TrimPrefix(c.TCPBase, ".")
	}
	return c.BaseDomain
}

func (c *Config) TCPURL(subdomain string) string {
	return subdomain + "." + c.TCPBaseDomain() + ":" + strconv.Itoa(c.TCPPort)
}

func getBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func getInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func getDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range splitOn(s, ',') {
		p = trimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitOn(s string, sep rune) []string {
	var parts []string
	cur := []rune{}
	for _, r := range s {
		if r == sep {
			parts = append(parts, string(cur))
			cur = cur[:0]
			continue
		}
		cur = append(cur, r)
	}
	parts = append(parts, string(cur))
	return parts
}

func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r' || s[start] == '\n') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r' || s[end-1] == '\n') {
		end--
	}
	return s[start:end]
}
