package api

import (
	"context"
	"net/http"
	"strings"

	"octoport/control-plane/internal/auth"
)

type ctxKey string

const ctxClaims ctxKey = "claims"

// authed parses the Bearer JWT, optionally enforces a scope, and stashes the
// claims in the request context.
func (s *Server) authed(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.bearerToken(r)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := s.Auth.Parse(token)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if scope != "" && claims.Scope != scope {
			writeErr(w, http.StatusForbidden, "insufficient token scope")
			return
		}
		ctx := context.WithValue(r.Context(), ctxClaims, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts the token from the configured auth header or query.
func (s *Server) bearerToken(r *http.Request) string {
	h := r.Header.Get(s.Cfg.AgentAuthHeader)
	if h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			return strings.TrimPrefix(h, "Bearer ")
		}
		return h
	}
	return r.URL.Query().Get("token")
}

func claimsFrom(ctx context.Context) (*auth.Claims, bool) {
	c, ok := ctx.Value(ctxClaims).(*auth.Claims)
	return c, ok
}
