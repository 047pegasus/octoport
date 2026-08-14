package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub OAuth sign-in. The desktop app cannot hold the client secret, so the
// whole dance runs on the control plane:
//
//	begin     the app asks for a fresh attempt -> { authorizeUrl, code }
//	          (opens the browser to authorizeUrl; state links back to the attempt)
//	callback  GitHub redirects here after consent; we exchange the code, fetch
//	          the user, upsert the account, and stash the JWT on the attempt
//	exchange  the app polls with its `code` until the attempt resolves, then
//	          receives the same shape as POST /auth/login
//
// Attempts live in the cache with a short TTL so nothing lingers server-side.

// oauthAttempt tracks one in-flight GitHub sign-in.
type oauthAttempt struct {
	AuthCode   string    `json:"authCode"`
	State      string    `json:"state"`
	Status     string    `json:"status"` // pending | success | error
	Error      string    `json:"error,omitempty"`
	Token      string    `json:"token,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt,omitempty"`
	UserID     string    `json:"userId,omitempty"`
	Email      string    `json:"email,omitempty"`
	Avatar     string    `json:"avatar,omitempty"`
	Plan       string    `json:"plan,omitempty"`
	MaxTunnels int       `json:"maxTunnels,omitempty"`
	// CliDevice, when set, marks this attempt as part of an `octoport login`
	// browser flow. On success the callback completes that CLI session and
	// redirects the browser to the done page instead of the generic one.
	CliDevice string `json:"cliDevice,omitempty"`
}

func oauthStateKey(state string) string { return "oauth:state:" + state }
func oauthCodeKey(code string) string   { return "oauth:code:" + code }

// githubOAuthEnabled reports whether GitHub sign-in has been configured.
func (s *Server) githubOAuthEnabled() bool {
	return s.Cfg.GitHubClientID != "" && s.Cfg.GitHubClientSecret != "" && s.Cfg.OAuthRedirectBase != ""
}

func (s *Server) githubRedirectURI() string {
	return strings.TrimRight(s.Cfg.OAuthRedirectBase, "/") + "/auth/github/callback"
}

// handleGitHubBegin creates a pending sign-in attempt and hands back the
// authorize URL for the browser plus the code the app should poll with.
func (s *Server) handleGitHubBegin(w http.ResponseWriter, r *http.Request) {
	if !s.githubOAuthEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "GitHub sign-in is not configured on this server")
		return
	}
	authCode := randToken(32)
	state := randToken(32)
	attempt := oauthAttempt{AuthCode: authCode, State: state, Status: "pending"}

	// An optional device code links this attempt to an `octoport login` browser
	// flow so the callback can resolve the CLI session directly.
	attempt.CliDevice = r.URL.Query().Get("device")

	ctx := r.Context()
	if err := s.Cache.SetJSON(ctx, oauthStateKey(state), attempt, s.Cfg.OAuthAttemptTTL); err != nil {
		s.Log.Error("github begin: store attempt failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not start sign-in")
		return
	}
	if err := s.Cache.SetJSON(ctx, oauthCodeKey(authCode), state, s.Cfg.OAuthAttemptTTL); err != nil {
		s.Log.Error("github begin: store code failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not start sign-in")
		return
	}

	q := url.Values{}
	q.Set("client_id", s.Cfg.GitHubClientID)
	q.Set("redirect_uri", s.githubRedirectURI())
	q.Set("scope", "user:email")
	q.Set("state", state)
	authorizeURL := s.Cfg.GitHubOAuthBase + "/login/oauth/authorize?" + q.Encode()

	writeJSON(w, http.StatusOK, map[string]any{
		"authorizeUrl": authorizeURL,
		"code":         authCode,
	})
}

// handleGitHubCallback is where GitHub redirects the browser after consent.
func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		s.writeOAuthPage(w, "Sign-in failed", "GitHub did not return a verification code. Please try again from the OctoPort app.")
		return
	}

	ctx := r.Context()
	var attempt oauthAttempt
	if err := s.Cache.GetJSON(ctx, oauthStateKey(state), &attempt); err != nil {
		s.writeOAuthPage(w, "Link expired", "This sign-in link has expired or is invalid. Please try again from the OctoPort app.")
		return
	}

	fail := func(msg, page string) {
		attempt.Status = "error"
		attempt.Error = msg
		_ = s.Cache.SetJSON(ctx, oauthStateKey(state), attempt, s.Cfg.OAuthExchangeTTL)
		s.writeOAuthPage(w, "Sign-in failed", page)
	}

	accessToken, err := s.githubAccessToken(ctx, code)
	if err != nil {
		s.Log.Warn("github callback: token exchange failed", "err", err)
		fail("could not verify your GitHub account", "Could not verify your GitHub account. Please try again.")
		return
	}

	gh, err := s.githubUser(ctx, accessToken)
	if err != nil {
		s.Log.Warn("github callback: user fetch failed", "err", err)
		fail("could not load your GitHub profile", "Could not load your GitHub profile. Please try again.")
		return
	}

	email := gh.Email
	if email == "" {
		email = s.githubPrimaryEmail(ctx, accessToken)
	}
	if email == "" {
		// GitHub never reveals an email for some accounts; their documented
		// noreply address still gives the account a stable unique identity.
		email = fmt.Sprintf("%d+%s@users.noreply.github.com", gh.ID, gh.Login)
	}
	email = strings.ToLower(strings.TrimSpace(email))

	user, err := s.DB.UpsertGitHubUser(ctx, email, strconv.FormatInt(gh.ID, 10), gh.Login, gh.AvatarURL, s.Cfg.MaxTunnelsPerUser)
	if err != nil {
		s.Log.Error("github callback: upsert user failed", "err", err)
		fail("could not create your account", "Could not create your account. Please try again.")
		return
	}

	token, exp, err := s.Auth.Issue(user.ID, user.Email, "api")
	if err != nil {
		s.Log.Error("github callback: issue token failed", "err", err)
		fail("could not issue session", "Could not start your session. Please try again.")
		return
	}

	attempt.Status = "success"
	attempt.Token = token
	attempt.ExpiresAt = exp
	attempt.UserID = user.ID
	attempt.Email = user.Email
	attempt.Avatar = gh.AvatarURL
	attempt.Plan = user.Plan
	attempt.MaxTunnels = user.MaxTunnels
	// Shorten the TTL: the attempt is only useful for the token handoff now.
	if err := s.Cache.SetJSON(ctx, oauthStateKey(state), attempt, s.Cfg.OAuthExchangeTTL); err != nil {
		s.Log.Warn("github callback: store result failed", "err", err)
	}
	_ = s.DB.LogEvent(ctx, user.ID, "user.login", map[string]any{"provider": "github"})

	// CLI browser flow: resolve the device session and bounce the browser to
	// the "you can go back to your terminal" page.
	if attempt.CliDevice != "" {
		if claims, err := s.Auth.Parse(token); err == nil {
			s.completeCLISession(attempt.CliDevice, token, claims, user)
		}
		http.Redirect(w, r, "/auth/cli/done?device="+url.QueryEscape(attempt.CliDevice), http.StatusFound)
		return
	}

	s.writeOAuthPage(w, "Signed in", "You can close this window and return to the OctoPort app.")
}

// handleGitHubExchange resolves a sign-in attempt the app is polling.
func (s *Server) handleGitHubExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Code == "" {
		writeErr(w, http.StatusBadRequest, "missing code")
		return
	}

	ctx := r.Context()
	var state string
	if err := s.Cache.GetJSON(ctx, oauthCodeKey(req.Code), &state); err != nil {
		writeErr(w, http.StatusGone, "sign-in link expired, please try again")
		return
	}
	var attempt oauthAttempt
	if err := s.Cache.GetJSON(ctx, oauthStateKey(state), &attempt); err != nil {
		writeErr(w, http.StatusGone, "sign-in link expired, please try again")
		return
	}

	switch attempt.Status {
	case "pending":
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending"})
	case "error":
		_ = s.Cache.Del(ctx, oauthCodeKey(req.Code))
		_ = s.Cache.Del(ctx, oauthStateKey(state))
		msg := attempt.Error
		if msg == "" {
			msg = "sign-in failed"
		}
		writeErr(w, http.StatusBadRequest, msg)
	case "success":
		_ = s.Cache.Del(ctx, oauthCodeKey(req.Code))
		_ = s.Cache.Del(ctx, oauthStateKey(state))
		writeJSON(w, http.StatusOK, map[string]any{
			"id":         attempt.UserID,
			"email":      attempt.Email,
			"plan":       attempt.Plan,
			"maxTunnels": attempt.MaxTunnels,
			"token":      attempt.Token,
			"expiresAt":  attempt.ExpiresAt,
			"avatar":     attempt.Avatar,
		})
	default:
		writeErr(w, http.StatusInternalServerError, "invalid sign-in state")
	}
}

// ---- GitHub API client ----

type githubUserInfo struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

func (s *Server) oauthHTTP() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}

func (s *Server) githubAccessToken(ctx context.Context, code string) (string, error) {
	payload, _ := json.Marshal(map[string]string{
		"client_id":     s.Cfg.GitHubClientID,
		"client_secret": s.Cfg.GitHubClientSecret,
		"code":          code,
		"redirect_uri":  s.githubRedirectURI(),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.Cfg.GitHubOAuthBase+"/login/oauth/access_token", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.oauthHTTP().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("github token error: %s %s", out.Error, out.ErrorDesc)
	}
	return out.AccessToken, nil
}

func (s *Server) githubUser(ctx context.Context, accessToken string) (*githubUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Cfg.GitHubAPIBase+"/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := s.oauthHTTP().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github user: status %d", resp.StatusCode)
	}
	var u githubUserInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

// githubPrimaryEmail resolves the account's canonical email, preferring a
// primary, verified address. Returns "" if GitHub exposes none.
func (s *Server) githubPrimaryEmail(ctx context.Context, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Cfg.GitHubAPIBase+"/user/emails", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := s.oauthHTTP().Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&emails); err != nil {
		return ""
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email
		}
	}
	for _, e := range emails {
		if e.Verified {
			return e.Email
		}
	}
	if len(emails) > 0 {
		return emails[0].Email
	}
	return ""
}

// writeOAuthPage renders a tiny standalone page for the browser tab so the
// user knows the sign-in finished and to return to the app.
func (s *Server) writeOAuthPage(w http.ResponseWriter, title, message string) {
	page := fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OctoPort · GitHub sign-in</title>
<style>
  :root{color-scheme:dark}
  body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
       background:#08080c;color:#f4f4f7;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:24px}
  .card{max-width:380px;text-align:center;padding:32px;border:1px solid #23232e;border-radius:18px;
        background:rgba(16,16,24,.86);backdrop-filter:blur(16px);box-shadow:0 24px 60px rgba(0,0,0,.45)}
  h1{font-size:18px;margin:16px 0 8px}
  p{color:#9696a8;font-size:14px;line-height:1.5;margin:0}
%s
</style>
</head>
<body>%s<div class="card">
  <div class="brand">%s<span class="logo">OctoPort</span></div>
  <h1>%s</h1>
  <p>%s</p>
</div></body></html>`, authBrandCSS+authBackgroundCSS, authBackgroundHTML, authLogoMark, title, message)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, page)
}

func randToken(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
