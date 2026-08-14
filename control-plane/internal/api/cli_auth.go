package api

import (
	"net/http"
	"strconv"
	"time"

	"octoport/control-plane/internal/auth"
	"octoport/control-plane/internal/db"
)

// cliSessionTTL bounds how long a browser-login session can live before the
// CLI's poll loop gives up.
const cliSessionTTL = 10 * time.Minute

// cliSession tracks one `octoport login` browser flow.
type cliSession struct {
	Device     string
	Done       bool
	Token      string
	Email      string
	Avatar     string
	Plan       string
	MaxTunnels int
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

// handleCLISessionCreate starts a browser-login session and returns the
// device code. The CLI opens `{api}/auth/cli/login?device=...` in the browser
// and then polls handleCLIPoll until the session is completed.
func (s *Server) handleCLISessionCreate(w http.ResponseWriter, r *http.Request) {
	device := randToken(16)
	s.sweepCLISessions()
	s.cliMu.Lock()
	s.cliSessions[device] = &cliSession{Device: device, CreatedAt: time.Now()}
	s.cliMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"device": device})
}

// handleCLIComplete stores the session token once the user has signed in on
// the browser page. The provided JWT is verified so the CLI only ever receives
// a real, live credential.
func (s *Server) handleCLIComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device string `json:"device"`
		Token  string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Device == "" || req.Token == "" {
		writeErr(w, http.StatusBadRequest, "missing device or token")
		return
	}

	claims, err := s.Auth.Parse(req.Token)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid token")
		return
	}
	user, err := s.DB.GetUser(r.Context(), claims.UserID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	if claims.Scope != "api" {
		writeErr(w, http.StatusForbidden, "token scope must be 'api'")
		return
	}

	s.sweepCLISessions()
	s.cliMu.Lock()
	if sess := s.cliSessions[req.Device]; sess != nil {
		sess.Done = true
		sess.Token = req.Token
		sess.Email = user.Email
		sess.Avatar = user.GitHubAvatar
		sess.Plan = user.Plan
		sess.MaxTunnels = user.MaxTunnels
		sess.ExpiresAt = claims.RegisteredClaims.ExpiresAt.Time
	}
	s.cliMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCLIPoll is what the CLI polls until a session resolves. It mirrors the
// GitHub exchange response shape so the client can log the user in.
func (s *Server) handleCLIPoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device string `json:"device"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Device == "" {
		writeErr(w, http.StatusBadRequest, "missing device")
		return
	}

	s.sweepCLISessions()
	s.cliMu.Lock()
	sess := s.cliSessions[req.Device]
	// A completed session is consumed on first successful poll, so the token
	// never lingers for a second reader.
	if sess != nil && sess.Done {
		delete(s.cliSessions, req.Device)
	}
	s.cliMu.Unlock()

	switch {
	case sess == nil:
		writeErr(w, http.StatusNotFound, "sign-in session expired, please try again")
	case !sess.Done:
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"id":         sess.Email,
			"email":      sess.Email,
			"plan":       sess.Plan,
			"maxTunnels": sess.MaxTunnels,
			"token":      sess.Token,
			"expiresAt":  sess.ExpiresAt,
			"avatar":     sess.Avatar,
		})
	}
}

// completeCLISession stores a session token from the GitHub OAuth callback so
// the CLI poll loop can resolve it. The browser is then redirected to the
// done page.
func (s *Server) completeCLISession(device, token string, claims *auth.Claims, user *db.User) {
	s.sweepCLISessions()
	s.cliMu.Lock()
	defer s.cliMu.Unlock()
	if sess := s.cliSessions[device]; sess != nil {
		sess.Done = true
		sess.Token = token
		sess.Email = user.Email
		sess.Avatar = user.GitHubAvatar
		sess.Plan = user.Plan
		sess.MaxTunnels = user.MaxTunnels
		sess.ExpiresAt = claims.RegisteredClaims.ExpiresAt.Time
	}
}

// sweepCLISessions drops expired sessions so the map doesn't grow unbounded.
func (s *Server) sweepCLISessions() {
	s.cliMu.Lock()
	defer s.cliMu.Unlock()
	cutoff := time.Now().Add(-cliSessionTTL)
	for device, sess := range s.cliSessions {
		if sess.CreatedAt.Before(cutoff) {
			delete(s.cliSessions, device)
		}
	}
}

// handleCLILoginPage serves the browser sign-in page for `octoport login`. It
// posts to the normal auth API (same origin) and completes the CLI session,
// keeping the whole flow self-contained behind one host.
func (s *Server) handleCLILoginPage(w http.ResponseWriter, r *http.Request) {
	device := r.URL.Query().Get("device")
	gitHubOn := s.githubOAuthEnabled() && s.Cfg.GitHubClientID != ""

	page := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OctoPort · sign in</title>
<style>
  :root{color-scheme:dark}
  *{box-sizing:border-box}
  body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:#08080c;color:#f4f4f7;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:24px}
  .card{width:100%;max-width:372px;background:rgba(16,16,24,.86);backdrop-filter:blur(16px);border:1px solid #23232e;border-radius:18px;padding:32px;box-shadow:0 24px 60px rgba(0,0,0,.45)}
  .sub{color:#9696a8;font-size:13px;margin:8px 0 20px;text-align:center}` + authBrandCSS + authBackgroundCSS + `
  label{display:block;font-size:11px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:#a0a0a0;margin:14px 0 6px}
  input{width:100%;background:#0f0f0f;border:1px solid #2c2c2c;border-radius:8px;color:#e8e8e8;font-size:14px;padding:10px 12px}
  input:focus{outline:none;border-color:#ccccff}
  button{width:100%;border:0;border-radius:8px;font-size:14px;font-weight:600;padding:11px;cursor:pointer;margin-top:18px}
  .primary{background:#ccccff;color:#1c1c3a}
  .primary:hover{background:#dedeff}
  .ghost{background:transparent;border:1px solid #333;color:#e8e8e8;margin-top:10px}
  .ghost:hover{border-color:#ccccff}
  .github{background:#0f0f0f;border:1px solid #333;color:#e8e8e8;display:flex;align-items:center;justify-content:center;gap:8px;margin-top:18px}
  .github:hover{border-color:#fff}
  .github svg{width:16px;height:16px;fill:currentColor}
  .switch{background:none;border:none;color:#ccccff;font-size:13px;margin-top:14px;width:auto;padding:0;cursor:pointer}
  .switch:hover{text-decoration:underline}
  .msg{font-size:13px;margin-top:14px;min-height:18px}
  .err{color:#ff6b6b}
  .ok{color:#7ee0a3}
  .note{margin-top:20px;padding-top:14px;border-top:1px solid #242424;color:#666;font-size:12px}
</style>
</head>
<body>
` + authBackgroundHTML + `
<div class="card">
  <div class="brand">` + authLogoMark + `<span class="logo">OctoPort</span></div>
  <div class="sub">Sign in to authorize the OctoPort CLI on this device</div>
  ` + gitButtonMarkup(gitHubOn, device) + `
  <div class="msg" id="msg"></div>
  <form id="auth" onsubmit="return onSubmit(event)">
    <label for="email">Email</label>
    <input id="email" type="email" autocomplete="username" required placeholder="you@example.com">
    <label for="password">Password</label>
    <input id="password" type="password" autocomplete="current-password" required placeholder="&bull;&bull;&bull;&bull;&bull;&bull;&bull;&bull;">
    <button class="primary" type="submit">Sign in</button>
    <button class="switch" type="button" id="toggle">need an account? register</button>
  </form>
  <div class="note">You'll return to your terminal automatically when this finishes.</div>
</div>
<script>
  const DEVICE = ` + jsString(device) + `;
  const msg = document.getElementById('msg');
  let registering = false;

  document.getElementById('toggle').addEventListener('click', function () {
    registering = !registering;
    this.textContent = registering ? 'have an account? sign in' : 'need an account? register';
    document.querySelector('button.primary').textContent = registering ? 'Create account' : 'Sign in';
  });

  function setMsg(text, ok) {
    msg.textContent = text;
    msg.className = 'msg ' + (ok ? 'ok' : 'err');
  }

  async function onSubmit(ev) {
    ev.preventDefault();
    const email = document.getElementById('email').value.trim();
    const password = document.getElementById('password').value;
    if (!email || !password) { setMsg('enter an email and password', false); return false; }
    const path = registering ? '/api/v1/auth/register' : '/api/v1/auth/login';
    setMsg('signing in…', true);
    try {
      const res = await fetch(path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email: email, password: password }),
      });
      const body = await res.json();
      if (!res.ok) { setMsg(body.error || 'sign-in failed', false); return false; }
      const done = await fetch('/api/v1/auth/cli/complete', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ device: DEVICE, token: body.token }),
      });
      if (!done.ok) { setMsg('could not finish sign-in', false); return false; }
      setMsg('signed in! you can close this tab.', true);
      window.location.href = '/auth/cli/done?device=' + encodeURIComponent(DEVICE);
    } catch (e) {
      setMsg('network error, please retry', false);
    }
    return false;
  }
</script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(page))
}

// handleCLIDonePage is where the browser lands after a successful sign-in,
// telling the user to return to their terminal.
func (s *Server) handleCLIDonePage(w http.ResponseWriter, r *http.Request) {
	page := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OctoPort · signed in</title>
<style>
  :root{color-scheme:dark}
  body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:#08080c;color:#f4f4f7;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:24px}
  .card{max-width:380px;text-align:center;padding:32px;border:1px solid #23232e;border-radius:18px;background:rgba(16,16,24,.86);backdrop-filter:blur(16px);box-shadow:0 24px 60px rgba(0,0,0,.45)}
  h1{font-size:18px;margin:16px 0 8px}
  p{color:#9696a8;font-size:14px;line-height:1.5;margin:0}
  .badge{margin-top:18px;display:inline-block;padding:6px 14px;border-radius:999px;background:rgba(167,139,250,.15);color:#a78bfa;font-size:13px;font-weight:600}` + authBrandCSS + authBackgroundCSS + `
</style>
</head>
<body>` + authBackgroundHTML + `<div class="card">
  <div class="brand">` + authLogoMark + `<span class="logo">OctoPort</span></div>
  <h1>You're signed in</h1>
  <p>Return to your terminal — the CLI will pick things up right away.</p>
  <div class="badge">✓ device authorized</div>
</div></body></html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(page))
}

// gitButtonMarkup renders the "Continue with GitHub" button iff OAuth is
// configured. Clicking it starts a CLI-aware GitHub attempt and opens the
// authorize URL.
func gitButtonMarkup(on bool, device string) string {
	if !on {
		return `<div class="msg err" style="margin-bottom:4px">GitHub sign-in isn't configured on this server</div>`
	}
	githubMark := `<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z"/></svg>`
	onclick := "fetch('/api/v1/auth/github/begin?device=' + encodeURIComponent(DEVICE), {method:'POST'})" +
		".then(function(r){return r.json()}).then(function(b){ if(b.authorizeUrl){window.location.href=b.authorizeUrl} else {setMsg(b.error||'could not start GitHub sign-in', false)} })" +
		".catch(function(){setMsg('network error, please retry', false)})"
	return `<button class="github" type="button" onclick="` + onclick + `">` + githubMark + ` Continue with GitHub</button>`
}

// jsString escapes a value for safe embedding inside a JS string literal. The
// device code is a hex token, so quoting the quotes and backslashes is enough.
func jsString(v string) string {
	return strconv.Quote(v)
}