package api

import (
	"net/http"
	"strings"
	"time"
	"unicode"

	"octoport/control-plane/internal/auth"
	"octoport/control-plane/internal/db"
)

type registerReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !validEmail(req.Email) {
		writeErr(w, http.StatusBadRequest, "invalid email")
		return
	}
	if len(req.Password) < s.Cfg.PasswordMinLength {
		writeErr(w, http.StatusBadRequest, "password too short")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not hash password")
		return
	}

	user, err := s.DB.CreateUser(r.Context(), req.Email, hash, s.Cfg.MaxTunnelsPerUser)
	if err == db.ErrConflict {
		writeErr(w, http.StatusConflict, "email already registered")
		return
	}
	if err != nil {
		s.Log.Error("register: create user failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not create user")
		return
	}

	token, exp, err := s.Auth.Issue(user.ID, user.Email, "api")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	_ = s.DB.LogEvent(r.Context(), user.ID, "user.register", map[string]any{"email": user.Email})

	writeJSON(w, http.StatusCreated, s.authResponse(user, token, exp))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	user, err := s.DB.GetUserByEmail(r.Context(), req.Email)
	if err != nil || !auth.CheckPassword(user.PasswordHash, req.Password) {
		// identical response for "no such user" and "bad password"
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, exp, err := s.Auth.Issue(user.ID, user.Email, "api")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	_ = s.DB.LogEvent(r.Context(), user.ID, "user.login", nil)

	writeJSON(w, http.StatusOK, s.authResponse(user, token, exp))
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())
	user, err := s.DB.GetUser(r.Context(), claims.UserID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                 user.ID,
		"email":              user.Email,
		"plan":               user.Plan,
		"maxTunnels":         user.MaxTunnels,
		"baseDomain":         s.Cfg.BaseDomain,
		"reservedSubdomains": s.Cfg.ReservedSubdomains,
		"avatar":             user.GitHubAvatar,
		"createdAt":          user.CreatedAt,
	})
}

// authResponse is the shared session payload returned by login / register and
// the GitHub exchange. `avatar` is the user's GitHub avatar URL, empty for
// email/password accounts.
func (s *Server) authResponse(user *db.User, token string, exp time.Time) map[string]any {
	return map[string]any{
		"id":         user.ID,
		"email":      user.Email,
		"plan":       user.Plan,
		"maxTunnels": user.MaxTunnels,
		"token":      token,
		"expiresAt":  exp,
		"avatar":     user.GitHubAvatar,
	}
}

// handleRefresh reissues the caller's api token before it expires. Desktop
// clients call this periodically so users stay signed in until they log out,
// without ever re-entering their password.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())
	token, exp, err := s.Auth.Issue(claims.UserID, claims.Email, "api")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	_ = s.DB.LogEvent(r.Context(), claims.UserID, "auth.token_refresh", nil)
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expiresAt": exp})
}

// handleAgentToken exchanges the caller's api-scoped token for a short-lived,
// agent-scoped token. Agents carry this narrower credential on the WebSocket
// so the two surfaces can be revoked independently.
func (s *Server) handleAgentToken(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())
	token, exp, err := s.Auth.Issue(claims.UserID, claims.Email, "agent")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	_ = s.DB.LogEvent(r.Context(), claims.UserID, "auth.agent_token", nil)
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expiresAt": exp})
}

func validEmail(email string) bool {
	if email == "" || len(email) > 254 {
		return false
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return false
	}
	for _, r := range email {
		if r <= unicode.MaxASCII && (r == '@' || r == '.' || r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			continue
		}
		return false
	}
	return true
}
