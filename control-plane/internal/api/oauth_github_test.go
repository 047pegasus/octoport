package api

import (
	"testing"

	"octoport/control-plane/internal/config"
)

func TestRandToken(t *testing.T) {
	for _, n := range []int{16, 32, 48} {
		tok := randToken(n)
		if len(tok) != n*2 {
			t.Fatalf("expected %d hex chars for %d bytes, got %d", n*2, n, len(tok))
		}
		if tok == randToken(n) {
			t.Fatalf("two tokens of %d bytes collided", n)
		}
	}
}

func TestGitHubRedirectURI(t *testing.T) {
	s := &Server{Cfg: &config.Config{OAuthRedirectBase: "https://octoport.dev/"}}
	if got := s.githubRedirectURI(); got != "https://octoport.dev/auth/github/callback" {
		t.Fatalf("unexpected redirect uri %q", got)
	}
}

func TestGitHubOAuthEnabled(t *testing.T) {
	cfg := &config.Config{
		GitHubClientID:     "client-id",
		GitHubClientSecret: "client-secret",
		OAuthRedirectBase:  "https://octoport.dev",
	}
	if !(&Server{Cfg: cfg}).githubOAuthEnabled() {
		t.Fatal("expected GitHub OAuth to be enabled")
	}
	for _, field := range []struct {
		name  string
		setID func()
	}{
		{"client id", func() { cfg.GitHubClientID = "" }},
		{"client secret", func() { cfg.GitHubClientSecret = "" }},
		{"redirect base", func() { cfg.OAuthRedirectBase = "" }},
	} {
		s := &Server{Cfg: cfg}
		field.setID()
		if s.githubOAuthEnabled() {
			t.Fatalf("expected GitHub OAuth disabled when %s empty", field.name)
		}
	}
}
