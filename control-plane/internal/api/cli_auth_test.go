package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"octoport/control-plane/internal/auth"
	"octoport/control-plane/internal/config"
	"octoport/control-plane/internal/db"
)

func TestCLISessionLifecycle(t *testing.T) {
	s := &Server{cliSessions: make(map[string]*cliSession)}

	// Start a session.
	create := httptest.NewRecorder()
	s.handleCLISessionCreate(create, httptest.NewRequest(http.MethodPost, "/api/v1/auth/cli/session", nil))
	if create.Code != http.StatusOK {
		t.Fatalf("session create: got %d, want 200", create.Code)
	}
	var body struct {
		Device string `json:"device"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &body); err != nil || body.Device == "" {
		t.Fatalf("session create: bad body %q", create.Body.String())
	}

	// Pending before completion.
	poll := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/cli/token",
			strings.NewReader(`{"device":"`+body.Device+`"}`))
		rec := httptest.NewRecorder()
		s.handleCLIPoll(rec, req)
		return rec.Code
	}
	if code := poll(); code != http.StatusAccepted {
		t.Fatalf("pending poll: got %d, want 202", code)
	}

	// Complete via the GitHub callback path (DB-free: we fabricate the user).
	user := &db.User{ID: "u1", Email: "ada@example.com", GitHubAvatar: "https://avatars/x.png", Plan: "free", MaxTunnels: 5}
	claims := &auth.Claims{UserID: "u1", Email: "ada@example.com"}
	claims.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	s.completeCLISession(body.Device, "jwt-token", claims, user)

	// First successful poll returns the session and consumes it.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/cli/token",
		strings.NewReader(`{"device":"`+body.Device+`"}`))
	rec := httptest.NewRecorder()
	s.handleCLIPoll(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("done poll: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Email string `json:"email"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("done poll: bad body %q", rec.Body.String())
	}
	if out.Email != "ada@example.com" || out.Token != "jwt-token" {
		t.Fatalf("done poll: unexpected payload %+v", out)
	}

	// A second poll finds the session consumed.
	if code := poll(); code != http.StatusNotFound {
		t.Fatalf("second poll: got %d, want 404", code)
	}
}

func TestCLILoginPageRenders(t *testing.T) {
	s := &Server{
		Cfg:         &config.Config{},
		cliSessions: make(map[string]*cliSession),
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/cli/login?device=abc123", nil)
	rec := httptest.NewRecorder()
	s.handleCLILoginPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login page: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "abc123") {
		t.Fatalf("login page: device code not embedded")
	}
}

func TestCLISweepExpiresSessions(t *testing.T) {
	s := &Server{cliSessions: make(map[string]*cliSession)}
	s.cliSessions["old"] = &cliSession{Device: "old", CreatedAt: time.Now().Add(-cliSessionTTL - time.Minute)}
	s.sweepCLISessions()
	if len(s.cliSessions) != 0 {
		t.Fatalf("expected expired session to be swept, got %d sessions", len(s.cliSessions))
	}
}
