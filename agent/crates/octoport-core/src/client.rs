//! REST client for the control plane: auth and tunnel lifecycle. The CLI and
//! the native GUI share this so both control the same API surface.

use anyhow::{anyhow, Result};
use futures_util::{StreamExt, TryStreamExt};
use serde::{Deserialize, Serialize};

/// A signed-in identity and its bearer token.
///
/// `Debug` is implemented by hand rather than derived: this struct holds a
/// live credential, and a derived `Debug` would print it verbatim into any
/// panic message, error chain or log line that formats it with `{:?}`.
#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AuthResponse {
    pub id: String,
    pub email: String,
    pub plan: String,
    pub max_tunnels: i32,
    pub token: String,
    pub expires_at: String,
    /// GitHub avatar URL for OAuth accounts; empty for email/password.
    #[serde(default)]
    pub avatar: Option<String>,
}

impl std::fmt::Debug for AuthResponse {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("AuthResponse")
            .field("id", &self.id)
            .field("email", &self.email)
            .field("plan", &self.plan)
            .field("max_tunnels", &self.max_tunnels)
            .field("token", &"<redacted>")
            .field("expires_at", &self.expires_at)
            .field("avatar", &self.avatar)
            .finish()
    }
}

/// Response of `POST /api/v1/auth/refresh` and `POST /api/v1/auth/agent-token`:
/// the control plane reissues a token and returns only that plus its expiry.
///
/// `Debug` is redacted for the same reason as [`AuthResponse`].
#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TokenRefresh {
    pub token: String,
    pub expires_at: String,
}

impl std::fmt::Debug for TokenRefresh {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("TokenRefresh")
            .field("token", &"<redacted>")
            .field("expires_at", &self.expires_at)
            .finish()
    }
}

/// Response of `GET /api/v1/me`: the signed-in account's profile and plan.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Me {
    pub id: String,
    pub email: String,
    pub plan: String,
    pub max_tunnels: i32,
    pub base_domain: String,
    #[serde(default)]
    pub reserved_subdomains: Vec<String>,
    #[serde(default)]
    pub avatar: Option<String>,
}

/// A tunnel as returned by the control plane.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Tunnel {
    pub id: String,
    pub subdomain: String,
    pub url: String,
    pub protocol: String,
    pub local_addr: String,
    pub expires_at: String,
}

/// Response of `POST /api/v1/auth/github/begin`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GitHubBegin {
    pub authorize_url: String,
    pub code: String,
}

/// Response of `POST /api/v1/auth/cli/session`. The CLI opens `login_uri` in
/// the browser and polls with the device code until the user signs in.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CliLogin {
    pub device: String,
}

/// Result of polling `POST /api/v1/auth/github/exchange`.
#[derive(Debug, Clone)]
pub enum GitHubPoll {
    /// The browser flow hasn't finished yet; poll again.
    Pending,
    /// Sign-in succeeded; carries the same payload as `login`.
    Done(AuthResponse),
}

/// Result of polling an `octoport login` browser session.
#[derive(Debug, Clone)]
pub enum CliPoll {
    /// The browser flow hasn't finished yet; poll again.
    Pending,
    /// Sign-in succeeded; carries the same payload as `login`.
    Done(AuthResponse),
}

/// A tunnel listed by `GET /api/v1/tunnels`. "stats" SSE frames only carry a
/// subset of these fields, so everything except `subdomain` is defaulted to
/// stay parseable for both shapes.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TunnelListItem {
    #[serde(default)]
    pub id: String,
    pub subdomain: String,
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub protocol: String,
    #[serde(default)]
    pub local_addr: String,
    #[serde(default)]
    pub bound: bool,
    #[serde(default)]
    pub enabled: bool,
    #[serde(default)]
    pub expires_at: String,
    #[serde(default)]
    pub requests: u64,
    #[serde(default)]
    pub bytes_in: u64,
    #[serde(default)]
    pub bytes_out: u64,
    #[serde(default)]
    pub last_active_at: i64,
}

/// One row of the caller's activity history.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EventItem {
    pub id: String,
    pub kind: String,
    #[serde(default)]
    pub payload: serde_json::Value,
    pub created_at: String,
}

/// A frame pushed by the control plane's SSE stream (`GET /api/v1/events/stream`).
/// `kind` is one of "snapshot", "list" or "stats"; each carries a tunnel list.
#[derive(Debug, Clone, PartialEq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SseFrame {
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(default)]
    pub tunnels: Vec<TunnelListItem>,
}

#[derive(Debug, Clone, Deserialize)]
struct ListTunnels {
    tunnels: Vec<TunnelListItem>,
}

#[derive(Debug, Clone, Deserialize)]
struct ListEvents {
    events: Vec<EventItem>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct ErrorBody {
    error: String,
}

/// An API-level failure carrying the HTTP status alongside a clean,
/// user-facing message. Displaying the error renders just the message so the
/// GUI can toast it verbatim.
#[derive(Debug, Clone)]
pub struct ApiError {
    pub status: u16,
    pub message: String,
}

impl std::fmt::Display for ApiError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.message)
    }
}

impl std::error::Error for ApiError {}

/// Inspect an error for its HTTP status, if it came from the API.
pub fn api_status(err: &anyhow::Error) -> Option<u16> {
    err.downcast_ref::<ApiError>().map(|e| e.status)
}

/// A thin, sync-free HTTP client for talking to the control plane.
#[derive(Debug, Clone)]
pub struct Client {
    http: reqwest::Client,
    api_url: String,
    token: Option<String>,
}

impl Client {
    pub fn new(api_url: impl Into<String>) -> Self {
        Client {
            http: reqwest::Client::builder()
                .connect_timeout(std::time::Duration::from_secs(10))
                .build()
                .expect("valid reqwest client"),
            api_url: api_url.into().trim_end_matches('/').to_string(),
            token: None,
        }
    }

    /// Attach the bearer token used for subsequent authenticated calls.
    pub fn with_token(mut self, token: &str) -> Self {
        self.token = Some(token.to_string());
        self
    }

    pub fn api_url(&self) -> &str {
        &self.api_url
    }

    pub async fn register(&self, email: &str, password: &str) -> Result<AuthResponse> {
        self.post_json("/api/v1/auth/register", &serde_json::json!({"email": email, "password": password}))
            .await
    }

    pub async fn login(&self, email: &str, password: &str) -> Result<AuthResponse> {
        self.post_json("/api/v1/auth/login", &serde_json::json!({"email": email, "password": password}))
            .await
    }

    /// Kick off a GitHub OAuth sign-in. Returns the URL to open in a browser
    /// and a one-time code to poll with until the flow resolves.
    pub async fn github_begin(&self) -> Result<GitHubBegin> {
        let url = format!("{}/api/v1/auth/github/begin", self.api_url);
        let resp = self.http.post(&url).send().await?;
        if !resp.status().is_success() {
            return Err(api_error(resp).await);
        }
        Ok(resp.json().await?)
    }

    /// Poll a GitHub sign-in attempt. Pending means the browser flow is still
    /// running; Done carries the same session as `login`.
    pub async fn github_poll(&self, code: &str) -> Result<GitHubPoll> {
        let url = format!("{}/api/v1/auth/github/exchange", self.api_url);
        let resp = self
            .http
            .post(&url)
            .json(&serde_json::json!({ "code": code }))
            .send()
            .await?;
        match resp.status() {
            reqwest::StatusCode::OK => {
                let auth: AuthResponse = resp.json().await?;
                Ok(GitHubPoll::Done(auth))
            }
            reqwest::StatusCode::ACCEPTED => Ok(GitHubPoll::Pending),
            reqwest::StatusCode::GONE => Err(anyhow!("sign-in link expired, please try again")),
            _ => Err(api_error(resp).await),
        }
    }

    /// Start a browser-based CLI sign-in. Returns the device code to poll with
    /// and the browser URL that shows the sign-in page.
    ///
    /// The CLI opens `cli_login_uri(device)` in the user's browser — that page
    /// supports both email/password and GitHub, exactly like the desktop app's
    /// own sign-in — then polls `cli_login_poll` until it resolves.
    pub async fn cli_login_start(&self) -> Result<CliLogin> {
        let url = format!("{}/api/v1/auth/cli/session", self.api_url);
        let resp = self.http.post(&url).send().await?;
        if !resp.status().is_success() {
            return Err(api_error(resp).await);
        }
        Ok(resp.json().await?)
    }

    /// The browser URL where the user completes a CLI sign-in.
    pub fn cli_login_uri(&self, device: &str) -> String {
        format!("{}/auth/cli/login?device={}", self.api_url, device)
    }

    /// Poll a CLI browser sign-in attempt. Pending means the user hasn't
    /// finished in the browser yet; Done carries the same session as `login`.
    pub async fn cli_login_poll(&self, device: &str) -> Result<CliPoll> {
        let url = format!("{}/api/v1/auth/cli/token", self.api_url);
        let resp = self
            .http
            .post(&url)
            .json(&serde_json::json!({ "device": device }))
            .send()
            .await?;
        match resp.status() {
            reqwest::StatusCode::OK => {
                let auth: AuthResponse = resp.json().await?;
                Ok(CliPoll::Done(auth))
            }
            reqwest::StatusCode::ACCEPTED => Ok(CliPoll::Pending),
            reqwest::StatusCode::NOT_FOUND => Err(anyhow!("sign-in link expired, please try again")),
            _ => Err(api_error(resp).await),
        }
    }

    pub async fn me(&self) -> Result<Me> {
        let url = format!("{}{}", self.api_url, "/api/v1/me");
        let resp = self.auth_get(&url).await?;
        Ok(resp.json().await?)
    }

    /// Reissue the api-scoped token before it expires. Lets desktop clients
    /// keep a session alive until the user explicitly logs out.
    pub async fn refresh_token(&self) -> Result<TokenRefresh> {
        let url = format!("{}/api/v1/auth/refresh", self.api_url);
        let resp = self.auth_request(reqwest::Method::POST, &url).await?;
        if !resp.status().is_success() {
            return Err(api_error(resp).await);
        }
        Ok(resp.json().await?)
    }

    /// Exchange the api-scoped token for a short-lived, agent-scoped token
    /// used on the WebSocket ingress. Keeps the two surfaces independent.
    pub async fn agent_token(&self) -> Result<String> {
        let url = format!("{}/api/v1/auth/agent-token", self.api_url);
        let resp = self.auth_request(reqwest::Method::POST, &url).await?;
        if !resp.status().is_success() {
            return Err(api_error(resp).await);
        }
        let v: serde_json::Value = resp.json().await?;
        v["token"]
            .as_str()
            .map(|s| s.to_string())
            .ok_or_else(|| anyhow!("agent token missing from response"))
    }

    /// Create a tunnel exposing `local_addr` over `protocol`, optionally with
    /// a custom subdomain and a shorter lifetime cap (seconds).
    pub async fn create_tunnel(
        &self,
        local_addr: &str,
        protocol: &str,
        subdomain: Option<&str>,
        expires_in: Option<u64>,
    ) -> Result<Tunnel> {
        let mut body = serde_json::json!({"localAddr": local_addr, "protocol": protocol});
        if let Some(sub) = subdomain.filter(|s| !s.trim().is_empty()) {
            body["subdomain"] = sub.trim().to_string().into();
        }
        if let Some(secs) = expires_in.filter(|s| *s > 0) {
            body["expiresInSeconds"] = secs.into();
        }
        let tunnel: Tunnel = self.post_json("/api/v1/tunnels", &body).await?;
        Ok(tunnel)
    }

    pub async fn list_tunnels(&self) -> Result<Vec<TunnelListItem>> {
        let url = format!("{}{}", self.api_url, "/api/v1/tunnels");
        let resp = self.auth_get(&url).await?;
        let body: ListTunnels = resp.json().await?;
        Ok(body.tunnels)
    }

    pub async fn delete_tunnel(&self, id: &str) -> Result<()> {
        let url = format!("{}/api/v1/tunnels/{}", self.api_url, id);
        let resp = self.auth_request(reqwest::Method::DELETE, &url).await?;
        if !resp.status().is_success() {
            return Err(api_error(resp).await);
        }
        Ok(())
    }

    /// Pause or resume a tunnel without releasing its subdomain. A paused
    /// tunnel stops routing traffic but keeps its name reserved until the hard
    /// deadline or an explicit delete.
    pub async fn set_tunnel_enabled(&self, id: &str, enabled: bool) -> Result<()> {
        let url = format!("{}/api/v1/tunnels/{}", self.api_url, id);
        let body = serde_json::json!({ "enabled": enabled });
        let mut req = self.http.patch(&url).json(&body);
        if let Some(token) = &self.token {
            req = req.header("Authorization", format!("Bearer {token}"));
        }
        let resp = req.send().await?;
        if !resp.status().is_success() {
            return Err(api_error(resp).await);
        }
        Ok(())
    }

    /// Fetch the caller's recent activity history, newest first.
    pub async fn list_events(&self, limit: usize) -> Result<Vec<EventItem>> {
        let url = format!("{}/api/v1/events?limit={}", self.api_url, limit);
        let resp = self.auth_get(&url).await?;
        if !resp.status().is_success() {
            return Err(api_error(resp).await);
        }
        let body: ListEvents = resp.json().await?;
        Ok(body.events)
    }

    /// Subscribe to the control plane's SSE event stream. The stream stays
    /// open and yields a "snapshot" on connect, a fresh "list" whenever the
    /// caller's tunnels change, and "stats" frames every ~2s. It ends when the
    /// connection drops — callers reconnect with a short backoff.
    pub async fn stream_events(&self) -> Result<impl futures_util::Stream<Item = Result<SseFrame, anyhow::Error>>> {
        let url = format!("{}/api/v1/events/stream", self.api_url);
        let resp = self.auth_get(&url).await?;
        if !resp.status().is_success() {
            return Err(api_error(resp).await);
        }
        Ok(sse_frames(resp.bytes_stream().map_err(anyhow::Error::from)))
    }

    // ---- internals ----

    async fn post_json<T: serde::de::DeserializeOwned>(&self, path: &str, body: &serde_json::Value) -> Result<T> {
        let url = format!("{}{}", self.api_url, path);
        let mut req = self.http.post(&url).json(body);
        if let Some(token) = &self.token {
            req = req.header("Authorization", format!("Bearer {token}"));
        }
        let resp = req.send().await?;
        if !resp.status().is_success() {
            return Err(api_error(resp).await);
        }
        Ok(resp.json().await?)
    }

    async fn auth_get(&self, url: &str) -> Result<reqwest::Response> {
        self.auth_request(reqwest::Method::GET, url).await
    }

    async fn auth_request(&self, method: reqwest::Method, url: &str) -> Result<reqwest::Response> {
        let token = self.token.as_deref().ok_or_else(|| anyhow!("not authenticated; run `octoport login` first"))?;
        let resp = self
            .http
            .request(method, url)
            .header("Authorization", format!("Bearer {token}"))
            .send()
            .await?;
        Ok(resp)
    }
}

async fn api_error(resp: reqwest::Response) -> anyhow::Error {
    let status = resp.status().as_u16();
    let text = resp.text().await.unwrap_or_default();
    let message = if let Ok(body) = serde_json::from_str::<ErrorBody>(&text) {
        body.error
    } else if text.trim().is_empty() {
        format!("request failed (HTTP {status})")
    } else {
        text.trim().to_string()
    };
    anyhow::Error::new(ApiError { status, message })
}

/// Turn a chunked HTTP body into a stream of parsed SSE frames.
fn sse_frames<S, B>(inner: S) -> impl futures_util::Stream<Item = Result<SseFrame, anyhow::Error>>
where
    S: futures_util::Stream<Item = Result<B, anyhow::Error>> + 'static,
    B: AsRef<[u8]> + 'static,
{
    futures_util::stream::unfold((Box::pin(inner), Vec::new()), |(mut inner, mut buf)| async move {
        loop {
            if let Some(json) = take_sse_event(&mut buf) {
                return match serde_json::from_str::<SseFrame>(&json) {
                    Ok(frame) => Some((Ok(frame), (inner, buf))),
                    Err(e) => Some((Err(anyhow::anyhow!("bad SSE frame: {e}: {json}")), (inner, buf))),
                };
            }
            match inner.next().await {
                Some(Ok(chunk)) => buf.extend_from_slice(chunk.as_ref()),
                Some(Err(e)) => return Some((Err(e), (inner, buf))),
                None => return None,
            }
        }
    })
}

/// Extracts one complete SSE event's `data:` payload from `buf`, consuming its
/// bytes. Returns `None` until a full event (data line followed by a blank
/// line) is buffered. Heartbeat comments (`: ping`) and leading blank lines are
/// dropped.
fn take_sse_event(buf: &mut Vec<u8>) -> Option<String> {
    loop {
        let mut data_lines: Vec<String> = Vec::new();
        let mut pos = 0usize;
        let mut skipped_blank = false;

        while pos < buf.len() {
            let end = match buf[pos..].iter().position(|&b| b == b'\n') {
                Some(i) => pos + i,
                None => break, // incomplete line: wait for more bytes
            };
            let mut line = &buf[pos..end];
            if let Some(stripped) = line.strip_suffix(b"\r") {
                line = stripped;
            }
            if line.is_empty() {
                buf.drain(..=end);
                if !data_lines.is_empty() {
                    return Some(data_lines.join("\n"));
                }
                skipped_blank = true;
                break;
            }
            if let Some(d) = line.strip_prefix(b"data:") {
                data_lines.push(String::from_utf8_lossy(d.trim_ascii_start()).into_owned());
            }
            pos = end + 1;
        }

        if !skipped_blank {
            return None;
        }
    }
}
