//! The Dioxus desktop app: a sign-in screen and a modern dashboard.
//!
//! A single persistent agent connection serves every tunnel the user opens, so
//! the list auto-refreshes and new tunnels are routable immediately. The UI is
//! HTML/CSS inside a system WebView; all state and networking run natively.

use std::collections::{HashMap, VecDeque};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use dioxus::core::{Task, spawn_forever};
use dioxus::desktop::{DesktopContext, use_window};
#[cfg(not(target_os = "linux"))]
use dioxus::desktop::{
    icon_from_memory, use_tray_menu_event_handler,
    trayicon::{
        DioxusTrayIcon, init_tray_icon,
        menu::{Menu, MenuItem, PredefinedMenuItem, Submenu},
    },
};
use dioxus::document::eval;
use dioxus::prelude::*;
use octoport_core::client::{EventItem, GitHubPoll, TunnelListItem};
use octoport_core::store::{self, StoredAuth};
use octoport_core::{Client, Settings};
use futures_util::StreamExt;

use crate::theme;

#[derive(PartialEq, Clone, Copy)]
enum Tab {
    Tunnels,
    Usage,
}

#[derive(PartialEq, Clone, Copy)]
enum ThemeMode {
    System,
    Dark,
    Light,
}

fn theme_from_str(s: &str) -> ThemeMode {
    match s {
        "dark" => ThemeMode::Dark,
        "light" => ThemeMode::Light,
        _ => ThemeMode::System,
    }
}

#[derive(Clone, Copy, PartialEq)]
enum ToastKind {
    Success,
    Error,
    Info,
}

#[derive(Clone)]
struct Toast {
    id: u64,
    text: String,
    kind: ToastKind,
    born: Instant,
}

/// Diagnostic logging that compiles away entirely in release builds.
///
/// The shipped client should be quiet: users see errors as in-app toasts, and
/// anything written to stderr in a bundled desktop app is invisible anyway.
/// Keeping these behind `debug_assertions` also guarantees that internal
/// details (endpoints, identifiers) never reach a production log.
macro_rules! debug_log {
    ($($arg:tt)*) => {
        #[cfg(debug_assertions)]
        {
            eprintln!("[octoport-app] {}", format!($($arg)*));
        }
    };
}

/// The public project repository (website links and the About dialog).
const GITHUB_URL: &str = "https://github.com/047pegasus/octoport";

/// Version string baked in at compile time (cargo pkg version).
const VERSION: &str = env!("CARGO_PKG_VERSION");

/// Rolling window size (in samples) for every realtime usage chart: the
/// initial placeholder payload, the SSE stats ring buffer (`tunnel_metrics`),
/// the animated series (`chart_anim`/`chart_tip`) and the x-axis labels must
/// all agree on this number, or the chart's point count changes out from under
/// it the moment live data starts arriving.
///
/// The control plane emits one "stats" frame per second, so this is also the
/// window's width in seconds (~2 minutes).
const CHART_WINDOW: usize = 120;

/// Interval between chart pushes into the webview. 50ms (20fps) is smooth to
/// the eye while costing an order of magnitude less than the 60fps animation
/// loop that produces the eased values.
const CHART_PUSH_MS: u64 = 50;

/// Force a chart push even when the payload is unchanged, every N frames
/// (~2s). See the push loop for why an unconditional dedup is unsafe.
const CHART_FORCE_EVERY: u32 = 40;

/// Ceiling for SSE reconnect backoff.
const SSE_BACKOFF_MAX_SECS: u64 = 60;

/// How often the insight modal refetches the event log. This is a REST call
/// over the same long link as everything else, so it is deliberately slow:
/// events are a human-readable audit trail, not a realtime feed.
const INSIGHT_EVENTS_POLL_SECS: u64 = 15;

/// Small pseudo-random spread (0..=25% of the interval) so that many clients
/// reconnecting after the same outage don't all land on the same instant.
/// Derived from the clock rather than a PRNG dependency.
fn jitter_millis(interval_secs: u64) -> u64 {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.subsec_nanos() as u64)
        .unwrap_or(0);
    let span = (interval_secs * 1000) / 4 + 1;
    nanos % span
}

// ---- state ----

/// Create a [`Signal`] owned by the ROOT scope instead of the current
/// component scope.
///
/// `spawn_forever` tasks (used by tunnel create/delete) run in the root scope,
/// which is an *ancestor* of the `App` component scope. Signals created with
/// `use_signal` in `App` would be used from that ancestor scope — Dioxus warns
/// (`__copy_value_hoisted`) because the value may be dropped before the task
/// finishes. Hoisting the owner to the root scope keeps the signal alive for
/// the whole app and makes every scope a descendant (or the owner itself).
fn use_root_signal<T: 'static>(f: impl FnOnce() -> T) -> Signal<T> {
    use_hook(move || Signal::new_in_scope(f(), ScopeId::ROOT))
}

#[derive(Clone, Copy, PartialEq)]
struct AppState {
    auth: Signal<Option<StoredAuth>>,
    email: Signal<String>,
    password: Signal<String>,
    register_mode: Signal<bool>,
    auth_busy: Signal<bool>,
    github_busy: Signal<bool>,

    tab: Signal<Tab>,
    local_addr: Signal<String>,
    protocol: Signal<String>,
    tunnels: Signal<Vec<TunnelListItem>>,
    /// Rolling window of request-rate samples per tunnel (requests/2s tick),
    /// keyed by subdomain. The aggregate view sums across all tunnels.
    tunnel_metrics: Signal<HashMap<String, VecDeque<u64>>>,
    /// Monotonically increasing per-subdomain counter, bumped once every time
    /// a real SSE "stats" tick lands for that subdomain (regardless of
    /// whether the delta value actually changed). The 60fps animation loop
    /// compares this against its own last-seen snapshot to detect "a new
    /// tick really did arrive" — comparing the *value* instead (as an
    /// earlier version of this code did) meant two consecutive identical
    /// deltas (overwhelmingly the common case: two ticks with 0 new
    /// requests) looked like "nothing changed" and the chart never
    /// advanced/rolled during any quiet period, which is most of the time.
    metric_gen: Signal<HashMap<String, u64>>,
    /// Last seen cumulative request count per subdomain, used to compute the
    /// per-tick deltas that feed `tunnel_metrics`.
    prev_requests: Signal<HashMap<String, u64>>,
    /// Committed (settled/frozen) history per tunnel, oldest to newest,
    /// capped at `CHART_WINDOW - 1` samples. The 60fps animation loop
    /// appends one new frozen point per real SSE tick (see `metric_gen`
    /// above) and pops the oldest once the window is full, so the series
    /// starts empty and grows from the left, then rolls once full. The
    /// current, still-animating sample lives separately in `chart_tip` and
    /// is appended on top of this when serialized to the chart.
    chart_anim: Signal<HashMap<String, VecDeque<f32>>>,
    /// Per-tunnel "chasing tip": the newest sample keeps gliding toward the
    /// latest SSE target even between frames (not just when a tick lands), so
    /// the right edge of every series moves continuously instead of freezing
    /// for the tick interval. `(from, to, t)` where t eases 0→1.
    chart_tip: Signal<HashMap<String, (f32, f32, f32)>>,
    /// Subdomain of the tunnel whose insight window is open ("" = none). The
    /// window is a floating modal with a live chart, metrics and recent events.
    insight_sub: Signal<String>,
    /// Recent activity events shown inside the insight window.
    insight_events: Signal<Vec<EventItem>>,
    /// Public base URL suffix (e.g. "localhost:8090" locally), from the server.
    base_domain: Signal<String>,
    /// Subdomain labels that belong to other services on the base domain and
    /// may never be allocated as tunnels (from the server).
    reserved_subdomains: Signal<Vec<String>>,
    /// Free-plan tunnel quota, from the server.
    max_tunnels: Signal<i32>,
    theme_mode: Signal<ThemeMode>,
    dark: Signal<bool>,
    connected: Signal<bool>,

    settings_open: Signal<bool>,
    new_tunnel_open: Signal<bool>,
    about_open: Signal<bool>,
    new_subdomain: Signal<String>,
    new_expiry: Signal<u64>,

    toasts: Signal<Vec<Toast>>,
    toast_seq: Signal<u64>,

    agent_handle: Signal<Option<Task>>,
    refresh_handle: Signal<Option<Task>>,
}

impl AppState {
    fn settings() -> Settings {
        Settings::load().unwrap_or_default()
    }

    fn toast(mut self, kind: ToastKind, text: impl Into<String>) {
        let mut seq = (self.toast_seq)();
        seq += 1;
        self.toast_seq.set(seq);
        let mut toasts = (self.toasts)();
        toasts.push(Toast { id: seq, text: text.into(), kind, born: Instant::now() });
        while toasts.len() > 5 {
            toasts.remove(0);
        }
        self.toasts.set(toasts);
    }

    fn start_agent(mut self) {
        if (self.agent_handle)().is_some() {
            return;
        }
        let settings = Self::settings();
        let api_url = settings.api_url.clone();
        let ws_url = settings.ws_url.clone();
        let max_frame_size = settings.max_frame_size;
        let max_streams = settings.max_streams;
        let state = self;
        let mut connected = self.connected;
        let toast = self;

        let handle = spawn(async move {
            // Read the current token each pass so a rotation picked up by the
            // refresh loop keeps the agent working past the 24h TTL.
            while let Some(auth) = (state.auth)() {
                let client = Client::new(api_url.clone()).with_token(&auth.token);
                let agent_token = match client.agent_token().await {
                    Ok(t) => t,
                    Err(e) => {
                        toast.toast(ToastKind::Error, format!("agent token failed: {e:#}"));
                        tokio::time::sleep(Duration::from_secs(3)).await;
                        continue;
                    }
                };
                match octoport_core::Agent::connect(&ws_url, &agent_token, max_frame_size, max_streams).await {
                    Ok(agent) => {
                        connected.set(true);
                        let _ = agent.run().await;
                        connected.set(false);
                    }
                    Err(e) => {
                        toast.toast(ToastKind::Error, format!("agent error: {e:#}"));
                    }
                }
                tokio::time::sleep(Duration::from_secs(2)).await;
            }
        });
        self.agent_handle.set(Some(handle));
    }

    fn stop_agent(mut self) {
        if let Some(task) = self.agent_handle.take() {
            task.cancel();
        }
        self.connected.set(false);
    }

    fn start_refresh(mut self) {
        if (self.refresh_handle)().is_some() {
            return;
        }
        let api_url = Self::settings().api_url;
        let mut state = self;

        let handle = spawn(async move {
            // Rotate the api token well before the 24h expiry so sessions
            // survive until the user logs out. Tunnel state itself is pushed
            // by the control plane over a long-lived SSE stream — no polling.
            let mut last_refresh = Instant::now() - Duration::from_secs(6 * 3600);
            // Reconnect backoff state, reset on every successful connect.
            let mut backoff_secs: u64 = 0;
            let mut stream_failures: u32 = 0;
            while let Some(auth) = (state.auth)() {
                let client = Client::new(api_url.clone()).with_token(&auth.token);

                if last_refresh.elapsed() >= Duration::from_secs(6 * 3600) {
                    match client.refresh_token().await {
                        Ok(next) => {
                            let mut stored = auth.clone();
                            stored.token = next.token;
                            stored.expires_at = Some(next.expires_at);
                            let _ = store::save_auth(&stored);
                            state.auth.set(Some(stored));
                            last_refresh = Instant::now();
                        }
                        Err(e) if octoport_core::client::api_status(&e) == Some(401) => {
                            // Token expired and can't be renewed — drop the
                            // session and let the user sign in again.
                            let _ = store::clear_auth();
                            state.auth.set(None);
                            state.toast(ToastKind::Error, "session expired, please sign in again");
                            break;
                        }
                        Err(_) => { /* transient; retried on the next pass */ }
                    }
                }

                // Refresh account/profile metadata (quota, base domain) so the
                // UI always matches the server's configuration.
                if let Ok(me) = client.me().await {
                    state.base_domain.set(me.base_domain);
                    state.reserved_subdomains.set(me.reserved_subdomains.clone());
                    state.max_tunnels.set(me.max_tunnels);
                }

                // Subscribe to the server-pushed event stream: a "snapshot" on
                // connect, a fresh "list" whenever tunnels change, and "stats"
                // every ~2s. When the connection drops we reconnect shortly.
                match client.stream_events().await {
                    Ok(frames) => {
                        // A successful connect clears the backoff so the next
                        // blip reconnects promptly.
                        backoff_secs = 0;
                        stream_failures = 0;
                        let mut frames = Box::pin(frames);
                        while let Some(frame) = frames.next().await {
                            let Ok(frame) = frame else {
                                break; // transport hiccup; reconnect
                            };
                            match frame.kind.as_str() {
                                "snapshot" | "list" => {
                                    state.tunnels.set(frame.tunnels);
                                }
                                "stats" => {
                                    let mut list = (state.tunnels)();
                                    let mut prev = (state.prev_requests)();
                                    let mut tm = (state.tunnel_metrics)();
                                    let mut gen = (state.metric_gen)();
                                    for s in &frame.tunnels {
                                        if let Some(t) =
                                            list.iter_mut().find(|t| t.subdomain == s.subdomain)
                                        {
                                            t.requests = s.requests;
                                            t.bytes_in = s.bytes_in;
                                            t.bytes_out = s.bytes_out;
                                            t.last_active_at = s.last_active_at;
                                        }
                                        // Rate = cumulative delta since the last
                                        // tick, so the plot shows live spikes.
                                        let before = prev.get(&s.subdomain).copied().unwrap_or(s.requests);
                                        let delta = s.requests.saturating_sub(before);
                                        prev.insert(s.subdomain.clone(), s.requests);
                                        let q = tm.entry(s.subdomain.clone()).or_default();
                                        q.push_back(delta);
                                        if q.len() > CHART_WINDOW {
                                            q.pop_front();
                                        }
                                        // Bump the generation counter *every* tick,
                                        // independent of whether `delta` actually
                                        // changed. The animation loop uses this
                                        // (not the delta value) to decide "a new
                                        // sample really landed, commit the current
                                        // tip and start chasing the next one" —
                                        // comparing values instead would mean two
                                        // back-to-back idle ticks (delta 0, then
                                        // delta 0 again — the overwhelmingly common
                                        // case) looked identical to "nothing
                                        // happened", so the window never advanced
                                        // during any quiet period and the chart
                                        // looked permanently frozen.
                                        *gen.entry(s.subdomain.clone()).or_insert(0) += 1;
                                        if delta > 0 {
                                            log::debug!("sse stats: {} delta={} total={}", s.subdomain, delta, s.requests);
                                        }
                                    }
                                    // Drop histories for tunnels that are gone.
                                    let live: Vec<String> =
                                        list.iter().map(|t| t.subdomain.clone()).collect();
                                    tm.retain(|k, _| live.contains(k));
                                    gen.retain(|k, _| live.contains(k));
                                    state.tunnels.set(list);
                                    state.prev_requests.set(prev);
                                    state.tunnel_metrics.set(tm);
                                    state.metric_gen.set(gen);
                                }
                                _ => {}
                            }
                        }
                    }
                    Err(e) => {
                        // Only surface the first failure of a run. Without
                        // this, a control plane that is unreachable for a few
                        // minutes stacks a toast every retry.
                        stream_failures += 1;
                        if stream_failures == 1 {
                            state.toast(ToastKind::Error, format!("live updates unavailable: {e:#}"));
                        }
                    }
                }

                // Exponential backoff with jitter. The control plane may be a
                // home server across the public internet, so a tight 2s retry
                // from every client turns an outage or a restart into a
                // thundering herd right when the server can least afford it.
                // 2s -> 4s -> 8s ... capped at SSE_BACKOFF_MAX_SECS.
                backoff_secs = if backoff_secs == 0 { 2 } else { (backoff_secs * 2).min(SSE_BACKOFF_MAX_SECS) };
                let jitter = jitter_millis(backoff_secs);
                tokio::time::sleep(Duration::from_millis(backoff_secs * 1000 + jitter)).await;
            }
        });
        self.refresh_handle.set(Some(handle));
    }

    fn stop_refresh(mut self) {
        if let Some(task) = self.refresh_handle.take() {
            task.cancel();
        }
    }

    fn submit_auth(mut self) {
        let email = (self.email)().trim().to_string();
        let password = (self.password)();
        if email.is_empty() || password.is_empty() {
            self.toast(ToastKind::Error, "enter an email and password");
            return;
        }
        let register = (self.register_mode)();
        self.auth_busy.set(true);
        let api_url = Self::settings().api_url;
        let mut state = self;

        spawn(async move {
            let client = Client::new(api_url);
            let result = if register {
                client.register(&email, &password).await
            } else {
                client.login(&email, &password).await
            };
            match result {
                Ok(auth) => {
                    let stored = StoredAuth {
                        email: auth.email.clone(),
                        token: auth.token.clone(),
                        theme: Some(match (state.theme_mode)() {
                            ThemeMode::Dark => "dark".into(),
                            ThemeMode::Light => "light".into(),
                            ThemeMode::System => "system".into(),
                        }),
                        auth_provider: Some("email".into()),
                        avatar: auth.avatar.clone(),
                        expires_at: Some(auth.expires_at.clone()),
                        dark_mode: None,
                    };
                    if let Err(e) = store::save_auth(&stored) {
                        state.toast(ToastKind::Error, format!("could not persist token: {e}"));
                    }
                    state.auth.set(Some(stored));
                    state.password.set(String::new());
                    state.toast(ToastKind::Success, "signed in");
                }
                Err(e) => state.toast(ToastKind::Error, format!("{e:#}")),
            }
            state.auth_busy.set(false);
        });
    }

    fn sign_in_github(mut self) {
        if (self.auth_busy)() || (self.github_busy)() {
            return;
        }
        self.github_busy.set(true);
        let api_url = Self::settings().api_url;
        let mut state = self;

        spawn(async move {
            let client = Client::new(api_url);
            let begin = match client.github_begin().await {
                Ok(b) => b,
                Err(e) => {
                    state.github_busy.set(false);
                    let msg = if octoport_core::client::api_status(&e) == Some(404) {
                        "GitHub sign-in unavailable — the control plane seems out of date (route 404). Restart it after rebuilding.".to_string()
                    } else {
                        format!("GitHub sign-in unavailable: {e:#}")
                    };
                    state.toast(ToastKind::Error, msg);
                    return;
                }
            };
            open_browser(&begin.authorize_url);

            // The browser flow can sit on GitHub's page for a while; poll
            // until the callback resolves or the attempt expires (~10 min).
            let mut attempts = 0u32;
            loop {
                tokio::time::sleep(Duration::from_secs(2)).await;
                attempts += 1;
                match client.github_poll(&begin.code).await {
                    Ok(GitHubPoll::Pending) => {
                        if attempts > 300 {
                            state.github_busy.set(false);
                            state.toast(ToastKind::Error, "sign-in timed out, please try again");
                            return;
                        }
                    }
                    Ok(GitHubPoll::Done(auth)) => {
                        let stored = StoredAuth {
                            email: auth.email.clone(),
                            token: auth.token.clone(),
                            theme: Some(match (state.theme_mode)() {
                                ThemeMode::Dark => "dark".into(),
                                ThemeMode::Light => "light".into(),
                                ThemeMode::System => "system".into(),
                            }),
                            auth_provider: Some("github".into()),
                            avatar: auth.avatar.clone(),
                            expires_at: Some(auth.expires_at.clone()),
                            dark_mode: None,
                        };
                        if let Err(e) = store::save_auth(&stored) {
                            state.toast(ToastKind::Error, format!("could not persist token: {e}"));
                        }
                        state.auth.set(Some(stored));
                        state.password.set(String::new());
                        state.github_busy.set(false);
                        state.toast(ToastKind::Success, "signed in with GitHub");
                        return;
                    }
                    Err(e) => {
                        state.github_busy.set(false);
                        state.toast(ToastKind::Error, format!("{e:#}"));
                        return;
                    }
                }
            }
        });
    }

    fn logout(mut self) {
        let _ = store::clear_auth();
        self.auth.set(None);
        self.tunnels.set(vec![]);
        self.tunnel_metrics.set(HashMap::new());
        self.prev_requests.set(HashMap::new());
        self.chart_anim.set(HashMap::new());
        self.chart_tip.set(HashMap::new());
        self.insight_sub.set(String::new());
        self.insight_events.set(vec![]);
        self.toasts.set(vec![]);
        self.settings_open.set(false);
        self.new_tunnel_open.set(false);
        self.stop_agent();
        self.stop_refresh();
    }

    fn set_theme_mode(mut self, mode: ThemeMode) {
        if (self.theme_mode)() == mode {
            return;
        }
        self.theme_mode.set(mode);
        match mode {
            ThemeMode::Dark => self.dark.set(true),
            ThemeMode::Light => self.dark.set(false),
            ThemeMode::System => { /* the system watcher resolves this */ }
        }
        if let Some(auth) = (self.auth)() {
            let mut a = auth.clone();
            a.theme = Some(match mode {
                ThemeMode::Dark => "dark".into(),
                ThemeMode::Light => "light".into(),
                ThemeMode::System => "system".into(),
            });
            let _ = store::save_auth(&a);
        }
    }

    fn submit_new_tunnel(mut self) {
        let Some(auth) = (self.auth)() else {
            debug_log!("submit_new_tunnel: no auth");
            return;
        };
        let local_addr = self.normalize_addr();
        if local_addr.is_empty() {
            self.toast(ToastKind::Error, "enter a local address first");
            return;
        }
        let protocol = (self.protocol)();
        let subdomain = (self.new_subdomain)();
        let sub = if subdomain.trim().is_empty() {
            None
        } else {
            Some(subdomain.trim().to_string())
        };
        let expiry = (self.new_expiry)();

        // Guard rails before the round-trip: free-plan quota and duplicate
        // address+protocol (the server enforces both too, but failing fast
        // keeps the UI honest).
        let active = (self.tunnels)().len();
        let max = (self.max_tunnels)();
        if active >= max as usize {
            self.toast(ToastKind::Error, format!("tunnel limit reached ({max}) — close a tunnel first"));
            return;
        }
        let dup = (self.tunnels)().iter().any(|t| {
            t.protocol == protocol
                && t.local_addr
                    .split(':')
                    .last()
                    .is_some_and(|p| p == local_addr.split(':').last().unwrap_or(""))
        });
        if dup {
            self.toast(ToastKind::Error, format!("{protocol}://{local_addr} is already exposed"));
            return;
        }
        if let Some(s) = &sub {
            if (self.reserved_subdomains)().iter().any(|r| r.eq_ignore_ascii_case(s)) {
                self.toast(ToastKind::Error, format!("\"{s}\" is reserved for another service on this domain"));
                return;
            }
        }

        let api_url = Self::settings().api_url;
        let token = auth.token.clone();
        let mut state = self;

        debug_log!("create tunnel: addr={local_addr} proto={protocol} sub={sub:?}");
        // spawn_forever (not spawn): this task is created inside the AddTunnelModal
        // event handler, and the modal unmounts immediately below. Dioxus's
        // `spawn` cancels the task when its spawning component drops, which would
        // kill the in-flight HTTP request before it ever reaches the server.
        spawn_forever(async move {
            let result = Client::new(api_url)
                .with_token(&token)
                .create_tunnel(&local_addr, &protocol, sub.as_deref(), Some(expiry))
                .await;
            match result {
                Ok(t) => {
                    debug_log!("create tunnel OK: {} {}", t.subdomain, t.url);
                    // Show the tunnel immediately. The control plane also pushes
                    // an authoritative "list" frame over SSE, so this optimistic
                    // entry is replaced by (or deduped against) the server copy.
                    let item = TunnelListItem {
                        id: t.id.clone(),
                        subdomain: t.subdomain.clone(),
                        url: t.url.clone(),
                        protocol: t.protocol.clone(),
                        local_addr: t.local_addr.clone(),
                        bound: (state.connected)(),
                        enabled: true,
                        expires_at: t.expires_at.clone(),
                        requests: 0,
                        bytes_in: 0,
                        bytes_out: 0,
                        last_active_at: 0,
                    };
                    let mut list = (state.tunnels)();
                    if !list.iter().any(|x| x.id == item.id) {
                        list.push(item);
                        (state.tunnels).set(list);
                    }
                    state.toast(
                        ToastKind::Success,
                        format!("{} is live — {}", t.protocol, t.url),
                    );
                }
                Err(e) => {
                    debug_log!("create tunnel FAILED: {e:#}");
                    state.toast(ToastKind::Error, format!("create failed: {e:#}"));
                }
            }
        });
        self.local_addr.clear();
        self.new_subdomain.clear();
        self.new_tunnel_open.set(false);
    }

    fn delete_tunnel(&self, id: &str) {
        let Some(auth) = (self.auth)() else { return };
        let api_url = Self::settings().api_url;
        let subdomain = (self.tunnels)()
            .iter()
            .find(|t| t.id == id)
            .map(|t| t.subdomain.clone())
            .unwrap_or_else(|| "tunnel".into());
        let id = id.to_string();
        let token = auth.token.clone();
        let state = *self;

        // spawn_forever so the request can't be cancelled if the card unmounts
        // while the DELETE is still in flight.
        spawn_forever(async move {
            let result = Client::new(api_url).with_token(&token).delete_tunnel(&id).await;
            match result {
                Ok(()) => state.toast(ToastKind::Success, format!("{subdomain} closed")),
                Err(e) => state.toast(ToastKind::Error, format!("delete failed: {e:#}")),
            }
        });
    }

    fn open_insight(mut self, sub: &str) {
        self.insight_sub.set(sub.to_string());
        self.insight_events.set(vec![]);
    }

    fn close_insight(mut self) {
        self.insight_sub.set(String::new());
        self.insight_events.set(vec![]);
    }

    fn toggle_tunnel(&self, id: &str, enabled: bool) {
        let Some(auth) = (self.auth)() else { return };
        let api_url = Self::settings().api_url;
        let subdomain = (self.tunnels)()
            .iter()
            .find(|t| t.id == id)
            .map(|t| t.subdomain.clone())
            .unwrap_or_else(|| "tunnel".into());
        let id = id.to_string();
        let token = auth.token.clone();
        let mut state = *self;

        // Optimistic update so the switch flips instantly; the server copy
        // arrives on the next SSE "list" frame and reconciles any drift.
        let mut list = (self.tunnels)();
        if let Some(t) = list.iter_mut().find(|t| t.id == id) {
            t.enabled = enabled;
        }
        state.tunnels.set(list);

        spawn_forever(async move {
            let result = Client::new(api_url).with_token(&token).set_tunnel_enabled(&id, enabled).await;
            match result {
                Ok(()) => {
                    state.toast(
                        ToastKind::Success,
                        if enabled {
                            format!("{subdomain} resumed")
                        } else {
                            format!("{subdomain} paused — subdomain kept")
                        },
                    );
                }
                Err(e) => {
                    let mut list = (state.tunnels)();
                    if let Some(t) = list.iter_mut().find(|t| t.id == id) {
                        t.enabled = !enabled;
                    }
                    state.tunnels.set(list);
                    state.toast(ToastKind::Error, format!("toggle failed: {e:#}"));
                }
            }
        });
    }
fn normalize_addr(&self) -> String {
        let local = (self.local_addr)();
        let raw = local.trim();
        if raw.is_empty() {
            return String::new();
        }
        if raw.contains(':') {
            raw.to_string()
        } else {
            format!("localhost:{raw}")
        }
    }
}

// ---- root ----

#[allow(non_snake_case)]
pub fn App() -> Element {
    let stored = store::load_auth().ok().flatten();
    let theme_mode = stored
        .as_ref()
        .and_then(|a| a.theme.as_deref())
        .map(theme_from_str)
        .unwrap_or(ThemeMode::System);
    let dark = match theme_mode {
        ThemeMode::Dark => true,
        ThemeMode::Light => false,
        // System defaults dark until matchMedia reports the real preference.
        ThemeMode::System => true,
    };

    let state = AppState {
        auth: use_root_signal(move || stored.clone()),
        email: use_root_signal(String::new),
        password: use_root_signal(String::new),
        register_mode: use_root_signal(|| false),
        auth_busy: use_root_signal(|| false),
        github_busy: use_root_signal(|| false),
        tab: use_root_signal(|| Tab::Tunnels),
        local_addr: use_root_signal(|| "3000".into()),
        protocol: use_root_signal(|| "http".into()),
        tunnels: use_root_signal(Vec::new),
        tunnel_metrics: use_root_signal(HashMap::new),
        metric_gen: use_root_signal(HashMap::new),
        prev_requests: use_root_signal(HashMap::new),
        chart_anim: use_root_signal(HashMap::new),
        chart_tip: use_root_signal(HashMap::new),
        insight_sub: use_root_signal(String::new),
        insight_events: use_root_signal(Vec::new),
        base_domain: use_root_signal(|| "localhost".into()),
        reserved_subdomains: use_root_signal(Vec::new),
        max_tunnels: use_root_signal(|| 5),
        theme_mode: use_root_signal(move || theme_mode),
        dark: use_root_signal(move || dark),
        connected: use_root_signal(|| false),
        settings_open: use_root_signal(|| false),
        new_tunnel_open: use_root_signal(|| false),
        about_open: use_root_signal(|| false),
        new_subdomain: use_root_signal(String::new),
        new_expiry: use_root_signal(|| 0),
        toasts: use_root_signal(Vec::new),
        toast_seq: use_root_signal(|| 0),
        agent_handle: use_root_signal(|| None),
        refresh_handle: use_root_signal(|| None),
    };

    // Start the agent + refresh loops once a session exists, and tear them down
    // when the session ends.
    use_effect(move || {
        let Some(_) = (state.auth)() else {
            state.stop_agent();
            state.stop_refresh();
            return;
        };
        state.start_agent();
        state.start_refresh();
    });

    // Follow the OS light/dark preference while in "system" mode.
    use_effect(move || {
        spawn(async move {
            let mut dark = state.dark;
            loop {
                if (state.theme_mode)() == ThemeMode::System {
                    let handle = eval("matchMedia('(prefers-color-scheme: dark)').matches");
                    if let Ok(res) = handle.await {
                        if let Some(v) = res.as_bool() {
                            dark.set(v);
                        }
                    }
                }
                tokio::time::sleep(Duration::from_secs(3)).await;
            }
        });
    });

    // Bootstrap the rust-ui chart engine (React + recharts UMD bundles, then
    // the shadcn-style initializer) and keep <html data-theme> in sync so the
    // charts pick up the current palette. `eval` here runs in the WebView page
    // context; each library is defined synchronously first so the initializer
    // can use the globals it needs (React, ReactDOM, PropTypes, Recharts).
    use_effect(move || {
        spawn(async move {
            let _ = eval(include_str!("../assets/react.js")).await;
            let _ = eval(include_str!("../assets/react-dom.js")).await;
            let _ = eval(include_str!("../assets/prop-types.min.js")).await;
            let _ = eval(include_str!("../assets/recharts.js")).await;
            let _ = eval(include_str!("../assets/shadcn_init.js")).await;
            let _ = eval(&format!(
                "document.documentElement.setAttribute('data-theme', '{}');",
                if (state.dark)() { "dark" } else { "light" }
            ))
            .await;
        });
    });

    // Keep <html data-theme> current whenever the theme flips so the chart
    // engine re-initializes with the new palette (it watches that attribute).
    use_effect(move || {
        let dark = (state.dark)();
        let _ = eval(&format!(
            "document.documentElement.setAttribute('data-theme', '{}');",
            if dark { "dark" } else { "light" }
        ));
    });

    // Drop stale toasts every half second.
    use_effect(move || {
        spawn(async move {
            let mut toasts = state.toasts;
            loop {
                tokio::time::sleep(Duration::from_millis(500)).await;
                let now = Instant::now();
                let mut list = toasts();
                let before = list.len();
                list.retain(|t| now.duration_since(t.born) < Duration::from_secs(5));
                if list.len() != before {
                    toasts.set(list);
                }
            }
        });
    });

    #[cfg(target_os = "linux")]
    {
        use crate::linux_tray::{LinuxTray, TrayState, TunnelRow};
        use dioxus::core::{Runtime, ScopeId};
        use ksni::TrayMethods;
        use std::cell::RefCell;
        use std::rc::Rc;
        use std::sync::{Arc, Mutex};

        // Linux tray via ksni (StatusNotifierItem). Unlike the muda tray, ksni
        // serves a plain D-Bus menu: we push a fresh state snapshot whenever
        // tunnels/auth change (see below), which makes the host re-request the
        // menu. Activation commands (menu clicks, left click) arrive over `rx`
        // and are routed through the same handle_tray_event path as muda.
        //
        // Everything one-shot (channel, SNI service, command router) lives
        // inside the `use_hook` initializer so it runs exactly once per app
        // lifetime. Spawning these directly in the component body was the bug:
        // every re-render started another SNI service (=> a duplicate, stale
        // tray icon) and another receiver task.
        let win = use_window();
        let tray_handle = use_hook({
            let win = win.clone();
            move || {
                let (tray_tx, tray_rx) = std::sync::mpsc::channel::<String>();

            // Commands received by the pump thread are delivered to the main
            // thread through this queue.
            let command_q: Arc<Mutex<VecDeque<String>>> = Arc::new(Mutex::new(VecDeque::new()));

            let handle_slot: Rc<RefCell<Option<ksni::Handle<LinuxTray>>>> =
                Rc::new(RefCell::new(None));

                // Spawn the SNI service once. Must run inside the tokio
                // runtime, which dioxus spawn tasks do (the app's other spawn
                // blocks already use tokio timers).
                let handle_slot2 = handle_slot.clone();
                let spawn_tx = tray_tx.clone();
                let tray_state = state;
                spawn(async move {
                    let tray = LinuxTray::new(spawn_tx, TrayState::default());
                    match tray.spawn().await {
                        Ok(handle) => {
                            // Seed the menu with the current state before
                            // storing the handle, since the sync effect below
                            // already ran once on mount while the handle was
                            // still None.
                            let snapshot = TrayState {
                                email: (tray_state.auth)().map(|a| a.email.clone()),
                                tunnels: (tray_state.tunnels)()
                                    .iter()
                                    .map(|t| TunnelRow {
                                        id: t.id.clone(),
                                        subdomain: t.subdomain.clone(),
                                        enabled: t.enabled,
                                        bound: t.bound,
                                    })
                                    .collect(),
                            };
                            let _ = handle.update(move |t| t.update_state(snapshot)).await;
                            *handle_slot2.borrow_mut() = Some(handle);
                        }
                        Err(e) => {
                            log::warn!("system tray unavailable: {e}");
                        }
                    }
                });

                // Route tray commands into app state on the main thread.
                //
                // This must NOT run as a dioxus `spawn` task. While the window
                // is hidden to the tray, dioxus's `poll_vdom` blocks awaiting
                // an edit-flush acknowledgement from the (hidden) WebView, so
                // the render/scheduler never advances and dioxus `spawn` tasks
                // are starved — the old receiver loop silently swallowed every
                // click. Instead:
                //   * a dedicated thread drains the command channel into a
                //     shared queue, then wakes the tao event loop (and shows
                //     the window) via `set_visible` so this keeps working while
                //     hidden; and
                //   * a wry event handler runs on the main thread (invoked for
                //     every tao event, independent of the dioxus scheduler) and
                //     drains the queue into `handle_tray_event`.
                let window = win.window.clone();
                let q = command_q.clone();
                std::thread::Builder::new()
                    .name("tray-pump".into())
                    .spawn(move || {
                        let rx = tray_rx;
                        while let Ok(id) = rx.recv() {
                            // `.unwrap_or_else(|e| e.into_inner())` so an
                            // unrelated panic elsewhere can't poison the queue
                            // and silently kill the pump thread.
                            q.lock().unwrap_or_else(|e| e.into_inner()).push_back(id);
                            // Wake the event loop and un-hide the window. On
                            // Linux `request_redraw` won't do either: it posts
                            // to tao's redraw channel, which never wakes a GTK
                            // loop that is idle-blocked while the window is
                            // hidden (the dioxus scheduler is stalled too, so
                            // there are no Poll events either). `set_visible`
                            // routes through the glib channel, which always
                            // wakes the loop; showing the window also lets the
                            // WebView resume flushing edits, so the dioxus
                            // VDOM/scheduler un-stalls and the handler below
                            // can run the command.
                            window.set_visible(true);
                        }
                    })
                    .expect("spawn tray command pump thread");

                let pump_state = state;
                let pump_win = win.clone();
                let q = command_q.clone();
                // Run the handler inside the dioxus runtime scope, exactly like
                // dioxus's own `use_wry_event_handler` hook: `handle_tray_event`
                // reads/writes signals, which requires a live runtime/scope.
                // Without it the first action that touches state panics and
                // silently breaks the queue.
                let runtime = Runtime::current();
                let scope_id = ScopeId::ROOT;
                win.create_wry_event_handler(move |_, _| {
                    runtime.in_scope(scope_id, || {
                        let mut q = q.lock().unwrap_or_else(|e| e.into_inner());
                        while let Some(id) = q.pop_front() {
                            handle_tray_event(pump_state, pump_win.clone(), &id);
                        }
                    });
                });

                handle_slot
            }
        });

        // Keep the tray menu in sync with the tunnels and auth state.
        let sync_handle = tray_handle.clone();
        use_effect(move || {
            let _ = (state.tunnels)();
            let _ = (state.auth)();
            let snapshot = TrayState {
                email: (state.auth)().map(|a| a.email.clone()),
                tunnels: (state.tunnels)()
                    .iter()
                    .map(|t| TunnelRow {
                        id: t.id.clone(),
                        subdomain: t.subdomain.clone(),
                        enabled: t.enabled,
                        bound: t.bound,
                    })
                    .collect(),
            };
            if let Some(handle) = sync_handle.borrow().as_ref().cloned() {
                spawn(async move {
                    let _ = handle.update(move |tray| tray.update_state(snapshot)).await;
                });
            }
        });
    }

    // 60fps realtime animation loop. Model: `chart_anim` holds *committed*
    // (frozen) history, oldest to newest, capped at CHART_WINDOW-1 samples.
    // `chart_tip` holds exactly one still-animating point per tunnel — the
    // newest sample, easing smoothly toward the latest SSE value. Every real
    // SSE "stats" tick (detected via `metric_gen`, see below) commits the
    // tip's current value into history and starts a fresh chase toward the
    // new target. Series therefore start EMPTY and grow one point per tick
    // from the left; once history hits the cap, the oldest point is popped
    // on every further tick, giving a proper rolling window.
    //
    // Ticks are detected via `metric_gen` (a per-subdomain counter bumped
    // once per real SSE tick, regardless of the delta's value) rather than
    // by comparing the delta value itself. Comparing values was the earlier
    // (buggy) approach: two consecutive idle ticks both carry delta=0, which
    // is by far the most common case, so "did the value change" was almost
    // always "no" and the window simply never advanced — the chart looked
    // permanently frozen even while real, if sparse, traffic was flowing.
    use_effect(move || {
        spawn(async move {
            let targets = state.tunnel_metrics;
            let gens_sig = state.metric_gen;
            let tunnels = state.tunnels;
            let mut anim = state.chart_anim;
            let mut tips = state.chart_tip;
            let mut last_tick = Instant::now();
            let mut frame_count = 0u64;
            // Last generation we've already committed a point for, per
            // subdomain. Purely local loop bookkeeping — never needs to be a
            // Signal since nothing else reads it.
            let mut seen_gen: HashMap<String, u64> = HashMap::new();
            loop {
                // 60fps only matters while a chart is actually visible. When
                // it isn't, drop to 4fps: ticks are still committed in the
                // right order and the history stays correct, but the app stops
                // burning a core's worth of wakeups in the background. This
                // matters most on the machines this agent runs on, where the
                // GUI is a background process for hours at a time.
                let visible = (state.tab)() == Tab::Usage || !(state.insight_sub)().is_empty();
                tokio::time::sleep(Duration::from_millis(if visible { 16 } else { 250 })).await;
                let tm = targets();
                let gens = gens_sig();
                let tunnel_list = tunnels();
                let mut cur = anim();
                let mut tip_state = tips();
                let mut changed = false;
                let dt = last_tick.elapsed().as_secs_f32().clamp(0.001, 0.05);
                last_tick = Instant::now();

                // Ensure every live tunnel has an entry to draw, even before
                // its first metrics tick arrives. Starts genuinely empty —
                // no zero pre-fill — so the chart shows nothing until the
                // first real sample, then grows from the left.
                for t in &tunnel_list {
                    cur.entry(t.subdomain.clone()).or_default();
                    tip_state.entry(t.subdomain.clone()).or_insert((0.0, 0.0, 1.0));
                }

                for (sub, q) in &tm {
                    let target = *q.back().unwrap_or(&0) as f32;
                    let gen = *gens.get(sub).unwrap_or(&0);
                    let last_seen = seen_gen.entry(sub.clone()).or_insert(0);
                    let tip = tip_state.entry(sub.clone()).or_insert((0.0, target, 1.0));

                    if gen != *last_seen {
                        // A real tick landed: freeze the tip's current
                        // (possibly still mid-ease) value into history, then
                        // start a fresh chase toward the new target.
                        let settled = tip.0 + (tip.1 - tip.0) * ease_in_out(tip.2);
                        let a = cur.entry(sub.clone()).or_default();
                        a.push_back(settled);
                        if a.len() > CHART_WINDOW.saturating_sub(1) {
                            a.pop_front();
                        }
                        tip.0 = settled;
                        tip.1 = target;
                        tip.2 = 0.0;
                        *last_seen = gen;
                        changed = true;
                    }

                    // Keep the tip gliding toward its target every frame
                    // (not just on tick arrival) so the right edge of the
                    // curve is always moving, ~0.8s ease-in per tick.
                    if tip.2 < 1.0 {
                        tip.2 = (tip.2 + dt / 0.8).min(1.0);
                        changed = true;
                    }
                }
                // Drop series/tips/generations for tunnels that are gone.
                let live: Vec<String> = tm.keys().cloned().collect();
                let before = cur.len();
                cur.retain(|k, _| live.contains(k));
                if cur.len() != before {
                    changed = true;
                }
                let before = tip_state.len();
                tip_state.retain(|k, _| live.contains(k));
                if tip_state.len() != before {
                    changed = true;
                }
                seen_gen.retain(|k, _| live.contains(k));
                if changed {
                    let anim_len = cur.len();
                    let tip_len = tip_state.len();
                    anim.set(cur);
                    tips.set(tip_state);

                    frame_count += 1;
                    if frame_count % 375 == 0 { // ~every 6 seconds at 60fps
                        log::debug!("chart_anim frame: tunnels={}, anim_entries={}, tip_entries={}",
                            tunnel_list.len(), anim_len, tip_len);
                    }
                } else {
                    frame_count += 1;
                }
            }
        });
    });

    // ---- unified chart push loop -------------------------------------
    //
    // ONE loop drives every chart in the app. Each pass it builds a single
    // JSON batch and hands it to `window.__octoportUpdateCharts`, so a frame
    // costs one webview round-trip no matter how many charts are on screen.
    //
    // Three deliberate properties, each fixing a way the previous per-chart
    // loops could stall permanently:
    //
    //   * No handshake. The old loops awaited `__octoportWaitChart(id)` before
    //     their first push and bailed out with `return` on timeout, which
    //     killed that chart's updates for the whole lifetime of the component.
    //     The JS side now buffers payloads for charts that aren't mounted yet
    //     and applies them on mount, so pushing early is harmless and no
    //     handshake is needed.
    //
    //   * No dedup. The old loops skipped sending a payload identical to the
    //     last one. Combined with a dropped first push that meant an unchanged
    //     value could never be re-sent, so a chart could latch permanently in
    //     a blank state.
    //
    //   * No unbounded await. Every eval is wrapped in a timeout, so a wedged
    //     webview round-trip costs one frame instead of freezing the loop.
    //
    // The batch is built with serde_json and interpolated as a JS object
    // literal. That is what keeps the argument types honest: the previous
    // contract passed serialized JSON into a parameter the JS side then ran
    // `JSON.parse` on, so JS received an Array where it expected a String,
    // `JSON.parse` stringified it to "1,2,3", threw, and a bare catch
    // swallowed the failure. Every update in the app was discarded that way.
    use_effect(move || {
        spawn(async move {
            let mut last_json = String::new();
            let mut since_forced = 0u32;
            loop {
                tokio::time::sleep(Duration::from_millis(CHART_PUSH_MS)).await;

                // Nothing on screen consumes charts unless the Usage tab is
                // showing or the insight modal is open. Skipping the work
                // entirely keeps an idle, backgrounded app from doing 20
                // serialize-and-eval round trips a second forever.
                let sub = (state.insight_sub)();
                let usage_visible = (state.tab)() == Tab::Usage;
                if !usage_visible && sub.is_empty() {
                    tokio::time::sleep(Duration::from_millis(400)).await;
                    continue;
                }

                let mut batch = serde_json::Map::new();
                if usage_visible {
                    batch.insert("realtime-chart".to_string(), realtime_payload(&state, None));
                }
                if !sub.is_empty() {
                    batch.insert("insight-area".to_string(), realtime_payload(&state, Some(&sub)));
                    if let Some(v) = pie_live(&state, &sub) {
                        batch.insert("insight-pie".to_string(), v);
                    }
                    if let Some(v) = radar_live(&state, &sub) {
                        batch.insert("insight-radar".to_string(), v);
                    }
                    if let Some(v) = radial_live(&state, &sub) {
                        batch.insert("insight-radial".to_string(), v);
                    }
                }
                if batch.is_empty() {
                    continue;
                }

                let json = serde_json::Value::Object(batch).to_string();

                // Skip re-sending a byte-identical payload: while a tunnel is
                // idle nothing moves, so this collapses the steady-state cost
                // to nearly zero without affecting the animation (the eased
                // tip changes the payload on every frame while it is moving).
                //
                // A forced resend every CHART_FORCE_EVERY frames is the safety
                // valve. Plain dedup was a real bug once: a payload dropped
                // because the chart had not mounted yet could never be re-sent,
                // latching the chart blank forever. The periodic resend means
                // any lost push self-heals within a couple of seconds even if
                // the JS-side buffering ever fails.
                since_forced += 1;
                if json == last_json && since_forced < CHART_FORCE_EVERY {
                    continue;
                }
                last_json = json.clone();
                since_forced = 0;

                let js = format!("window.__octoportUpdateCharts({json});");
                let fut = std::future::IntoFuture::into_future(eval(&js));
                let _ = tokio::time::timeout(Duration::from_millis(500), fut).await;
            }
        });
    });

    rsx! {
        style { dangerous_inner_html: theme::themed_css() }
        div {
            class: "app",
            "data-theme": if (state.dark)() { "dark" } else { "light" },
            TitleBar { state }
            if (state.auth)().is_some() {
                Dashboard { state }
            } else {
                AuthScreen { state }
            }
            if (state.settings_open)() {
                SettingsModal { state }
            }
            if (state.about_open)() {
                AboutModal { state }
            }
            if (state.new_tunnel_open)() {
                AddTunnelModal { state }
            }
            if !(state.insight_sub)().is_empty() {
                InsightModal { state }
            }
            ToastStack { state }
        }
    }
}

// ---- system tray ----

/// IDs for the tray menu items. The running-tunnels entries carry the tunnel
/// id in their own id (`tunnel:<id>:pause` / `tunnel:<id>:resume`).
const TRAY_OPEN: &str = "open";
const TRAY_ABOUT: &str = "about";
const TRAY_SIGNIN: &str = "signin";
const TRAY_SIGNOUT: &str = "signout";
const TRAY_QUIT: &str = "quit";
const TRAY_TUNNEL_PREFIX: &str = "tunnel:";
const TRAY_PAUSE: &str = ":pause";
const TRAY_RESUME: &str = ":resume";

/// Build the system-tray menu from the current app state. Each running tunnel
/// gets a clickable row that pauses/resumes it, with its live status inline.
#[cfg(not(target_os = "linux"))]
fn build_tray_menu(state: &AppState) -> Menu {
    let menu = Menu::new();

    let open = MenuItem::with_id(TRAY_OPEN, "Open OctoPort", true, None);
    let about = MenuItem::with_id(TRAY_ABOUT, "About OctoPort", true, None);
    let _ = menu.append_items(&[&open, &about, &PredefinedMenuItem::separator()]);

    match (state.auth)() {
        Some(auth) => {
            let signed_in = MenuItem::with_id(
                "signed-in",
                format!("Signed in as {}", auth.email),
                false,
                None,
            );
            let signout = MenuItem::with_id(TRAY_SIGNOUT, "Sign out", true, None);
            let _ = menu.append_items(&[&signed_in, &signout, &PredefinedMenuItem::separator()]);
        }
        None => {
            let signin = MenuItem::with_id(TRAY_SIGNIN, "Sign in…", true, None);
            let _ = menu.append_items(&[&signin, &PredefinedMenuItem::separator()]);
        }
    }

    let tunnels = Submenu::with_id("tunnels", "Running Tunnels", true);
    let list = (state.tunnels)();
    if list.is_empty() {
        let _ = tunnels.append(&MenuItem::with_id("no-tunnels", "No running tunnels", false, None));
    } else {
        for t in &list {
            let (label, id) = if t.enabled {
                (
                    format!("{}  [{}]", t.subdomain, if t.bound { "live" } else { "awaiting agent" }),
                    format!("{TRAY_TUNNEL_PREFIX}{}{TRAY_PAUSE}", t.id),
                )
            } else {
                (
                    format!("{}  [paused]", t.subdomain),
                    format!("{TRAY_TUNNEL_PREFIX}{}{TRAY_RESUME}", t.id),
                )
            };
            let _ = tunnels.append(&MenuItem::with_id(id, label, true, None));
        }
    }
    let _ = menu.append_items(&[&tunnels, &PredefinedMenuItem::separator()]);

    let quit = MenuItem::with_id(TRAY_QUIT, "Quit OctoPort", true, None);
    let _ = menu.append(&quit);
    menu
}

/// Handle a click on a tray menu item. `win` is the main window used to
/// re-open the app from the tray.
fn handle_tray_event(mut state: AppState, win: DesktopContext, id: &str) {
    match id {
        TRAY_OPEN => {
            win.set_visible(true);
            win.set_focus();
        }
        TRAY_ABOUT => {
            state.about_open.set(true);
            win.set_visible(true);
            win.set_focus();
        }
        TRAY_SIGNIN => {
            win.set_visible(true);
            win.set_focus();
        }
        TRAY_SIGNOUT => state.logout(),
        TRAY_QUIT => quit_all(state),
        _ => {
            if let Some(rest) = id.strip_prefix(TRAY_TUNNEL_PREFIX) {
                let (tunnel_id, action) = if let Some(rest) = rest.strip_suffix(TRAY_PAUSE) {
                    (rest.to_string(), false)
                } else if let Some(rest) = rest.strip_suffix(TRAY_RESUME) {
                    (rest.to_string(), true)
                } else {
                    return;
                };
                toggle_tunnel(state, tunnel_id, action);
            }
        }
    }
}

/// Pause or resume a single tunnel from the tray, then let the SSE stream push
/// the fresh state back to the UI and tray.
fn toggle_tunnel(state: AppState, tunnel_id: String, enabled: bool) {
    let api_url = AppState::settings().api_url;
    // Capture the token on the main thread, then run the HTTP call on the
    // independent tokio runtime. A dioxus `spawn` task would freeze while the
    // window is hidden to the tray (its render/scheduler blocks awaiting an
    // edit-flush from the hidden WebView), so tray actions would silently do
    // nothing until the window is reopened.
    let Some(auth) = (state.auth)() else { return };
    let token = auth.token.clone();
    tokio::runtime::Handle::current().spawn(async move {
        let client = Client::new(api_url).with_token(&token);
        if let Err(e) = client.set_tunnel_enabled(&tunnel_id, enabled).await {
            log::error!("could not {} tunnel: {e:#}", if enabled { "resume" } else { "pause" });
        }
    });
}

/// Quit for real: pause every running tunnel so the subdomains are released,
/// then exit the process. This is the only path that closes the app — the
/// window close button just hides to the tray.
fn quit_all(state: AppState) {
    let api_url = AppState::settings().api_url;
    // Read state and exit/network on the independent tokio runtime (see
    // `toggle_tunnel`): this must keep working while the window is hidden.
    let Some(auth) = (state.auth)() else {
        std::process::exit(0);
    };
    let token = auth.token.clone();
    let enabled_ids: Vec<String> = (state.tunnels)()
        .iter()
        .filter(|t| t.enabled)
        .map(|t| t.id.clone())
        .collect();
    tokio::runtime::Handle::current().spawn(async move {
        let client = Client::new(api_url).with_token(&token);
        for id in enabled_ids {
            let _ = client.set_tunnel_enabled(&id, false).await;
        }
        std::process::exit(0);
    });
}

// ---- custom titlebar ----

#[component]
fn TitleBar(state: AppState) -> Element {
    let win = use_window();
    let w_drag = win.clone();
    let w_min = win.clone();
    let w_max = win.clone();
    let w_close = win.clone();
    let connected = (state.connected)();

    rsx! {
        div {
            class: "titlebar",
            onmousedown: move |_| w_drag.drag(),
            div { class: "titlebar-brand", span { class: "tb-logo", dangerous_inner_html: crate::logo::mark_markup() }, "OctoPort" }
            if (state.auth)().is_some() {
                if connected {
                    span { class: "tb-dot live", title: "agent connected" }
                } else {
                    span { class: "tb-dot", title: "connecting…" }
                }
            }
            div { class: "titlebar-spacer" }
            if (state.auth)().is_some() {
                ProfileMenu { state }
            }
            div { class: "titlebar-controls",
                button {
                    class: "tb-btn",
                    title: "minimize",
                    onmousedown: move |e| e.stop_propagation(),
                    onclick: move |_| w_min.set_minimized(true),
                    "—"
                }
                button {
                    class: "tb-btn",
                    title: "maximize / restore",
                    onmousedown: move |e| e.stop_propagation(),
                    onclick: move |_| w_max.toggle_maximized(),
                    "▢"
                }
                button {
                    class: "tb-btn close",
                    title: "close",
                    onmousedown: move |e| e.stop_propagation(),
                    onclick: move |_| w_close.close(),
                    "✕"
                }
            }
        }
    }
}

// ---- auth ----

#[component]
fn AuthScreen(state: AppState) -> Element {
    let busy = (state.auth_busy)();
    let github_busy = (state.github_busy)();
    let any_busy = busy || github_busy;
    let register = (state.register_mode)();

    // Start the SlicedWaves background once the screen is mounted, and stop it
    // when the screen unmounts (a session starts). The WebGL loop lives fully
    // in the webview; eval just boots and shuts it down.
    use_effect(move || {
        spawn(async move {
            let _ = eval(include_str!("../assets/sliced_waves.js")).await;
            let _ = eval(&format!(
                "window.__octoportSlicedWaves('auth-waves', {{ color1: '#7C6CFF', color2: '#3B1D9E', color3: '#B497CF', columns: 16, rows: 10, barThickness: 0.12, speed: 0.4, travel: 0.75, waveSpread: 0.9, rowOffset: 1.0, softness: 0.06, glow: 0, brightness: 1.0, contrast: 1.0, opacity: 0.55, orientation: 'horizontal', mouseInteraction: true, grain: true, grainIntensity: 0.04 }});"
            ))
            .await;
        });
    });
    use_drop({
        let _ = state;
        move || {
            let _ = eval("window.__octoportSlicedWavesStop()");
        }
    });

    rsx! {
        div { class: "auth",
            canvas { id: "auth-waves", class: "auth-waves" }
            div { class: "auth-card",
                div { class: "auth-brand",
                    div { class: "auth-logo", dangerous_inner_html: crate::logo::mark_markup() }
                    div { class: "brand", "OctoPort" }
                    div { class: "brand-sub", "Open local ports to the public internet" }
                }
                h2 { {if register { "Create account" } else { "Sign in" }} }
                button {
                    class: "btn github-btn",
                    disabled: any_busy,
                    onclick: move |_| state.sign_in_github(),
                    span { class: "github-mark",
                        svg { view_box: "0 0 16 16", width: "16", height: "16", fill: "currentColor",
                            path { d: "M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z" }
                        }
                    }
                    {if github_busy { "Opening GitHub…" } else { "Continue with GitHub" }}
                }
                div { class: "auth-divider", span { "or sign in with email" } }
                div { class: "field",
                    label { "EMAIL" }
                    input {
                        r#type: "email",
                        placeholder: "you@example.com",
                        value: (state.email)(),
                        disabled: any_busy,
                        oninput: move |e| state.email.set(e.value()),
                    }
                }
                div { class: "field",
                    label { "PASSWORD" }
                    input {
                        r#type: "password",
                        placeholder: "••••••••",
                        value: (state.password)(),
                        disabled: any_busy,
                        oninput: move |e| state.password.set(e.value()),
                        onkeydown: move |e| {
                            if e.code() == Code::Enter { state.submit_auth() }
                        },
                    }
                }
                button {
                    class: "btn btn-primary",
                    disabled: any_busy,
                    onclick: move |_| state.submit_auth(),
                    {if busy { "Working…" } else if register { "Create account" } else { "Sign in" }}
                }
                button {
                    class: "link",
                    disabled: any_busy,
                    onclick: move |_| state.register_mode.set(!(state.register_mode)()),
                    {if register { "have an account? sign in" } else { "need an account? register" }}
                }
            }
        }
    }
}

// ---- dashboard ----

#[component]
fn Dashboard(state: AppState) -> Element {
    rsx! {
        div { class: "body",
            div { class: "sidebar", Sidebar { state } }
            div { class: "main",
                div { class: "tabs",
                    button { class: if (state.tab)() == Tab::Tunnels { "tab active" } else { "tab" }, onclick: move |_| state.tab.set(Tab::Tunnels), "Tunnels" }
                    button { class: if (state.tab)() == Tab::Usage { "tab active" } else { "tab" }, onclick: move |_| state.tab.set(Tab::Usage), "Usage" }
                }
                div { class: "content",
                    match (state.tab)() {
                        Tab::Tunnels => rsx! { TunnelsTab { state } },
                        Tab::Usage => rsx! { UsageTab { state } },
                    }
                }
            }
        }
    }
}

#[component]
fn ProfileMenu(state: AppState) -> Element {
    let mut open = use_signal(|| false);
    let auth = (state.auth)();
    let email = auth.as_ref().map(|a| a.email.clone()).unwrap_or_default();
    let avatar = auth.and_then(|a| a.avatar).unwrap_or_default();
    let initial = email.chars().next().unwrap_or('?').to_uppercase().to_string();
    let win = use_window();

    rsx! {
        div {
            class: "profile",
            onmousedown: move |e| e.stop_propagation(),
            button {
                class: "profile-btn",
                onmousedown: move |e| e.stop_propagation(),
                onclick: move |_| open.set(!open()),
                title: email.clone(),
                if avatar.is_empty() {
                    {initial}
                } else {
                    img { class: "profile-avatar", src: avatar.clone(), alt: "avatar" }
                }
            }
            if open() {
                div { class: "menu",
                    div { class: "menu-label", {email.clone()} }
                    div { class: "menu-sep" }
                    button { class: "menu-item", onclick: move |_| { open.set(false); state.settings_open.set(true); }, "Settings" }
                    div { class: "menu-sep" }
                    button { class: "menu-item", onclick: move |_| { open.set(false); state.about_open.set(true); }, "About OctoPort" }
                    div { class: "menu-sep" }
                    button { class: "menu-item danger", onclick: move |_| { open.set(false); state.logout(); }, "Log out" }
                    button { class: "menu-item", onclick: move |_| { open.set(false); win.close(); }, "Hide to tray" }
                }
            }
        }
    }
}

#[component]
fn Sidebar(state: AppState) -> Element {
    let active = (state.tunnels)().len();
    let max = (state.max_tunnels)();
    let full = active >= max as usize;

    rsx! {
        div { class: "side-label",
            "NEW TUNNEL"
            div { class: "hint", "One agent connection serves every tunnel you open." }
        }
        button {
            class: if full { "btn btn-primary btn-add disabled" } else { "btn btn-primary btn-add" },
            disabled: full,
            title: if full { format!("free plan limit ({max} tunnels) reached — close one to continue") } else { "Open a new tunnel" },
            onclick: move |_| state.new_tunnel_open.set(true),
            if full { "Limit reached ({max})" } else { "+ New tunnel" }
        }
        div { class: "side-label",
            div { class: "hint",
                "{active}/{max} tunnels · dissolve after 5 min idle and end after 36 hours."
            }
        }
    }
}

// ---- modals ----

#[component]
fn SettingsModal(state: AppState) -> Element {
    let auth = (state.auth)();
    let email = auth.as_ref().map(|a| a.email.clone()).unwrap_or_default();
    let avatar = auth.as_ref().and_then(|a| a.avatar.clone()).unwrap_or_default();
    let provider = auth
        .as_ref()
        .and_then(|a| a.auth_provider.clone())
        .unwrap_or_else(|| "email".into());
    let mode = (state.theme_mode)();

    rsx! {
        div { class: "modal-overlay", onmousedown: move |_| state.settings_open.set(false),
            div { class: "modal settings", onmousedown: move |e| e.stop_propagation(),
                div { class: "modal-head",
                    div { class: "modal-title", "Settings" }
                    button { class: "modal-x", title: "close", onclick: move |_| state.settings_open.set(false), "✕" }
                }
                div { class: "settings-section",
                    div { class: "section-title", "Profile" }
                    div { class: "settings-avatar",
                        if avatar.is_empty() {
                            {email.chars().next().unwrap_or('?').to_uppercase().to_string()}
                        } else {
                            img { src: avatar.clone(), alt: "avatar" }
                        }
                    }
                    div { class: "setting-row",
                        div { class: "setting-lbl", "Signed in as" }
                        div { class: "setting-val", {email.clone()} }
                    }
                    div { class: "setting-row",
                        div { class: "setting-lbl", "Account" }
                        div { class: "setting-val",
                            {if provider == "github" { "GitHub" } else { "Email & password" }}
                        }
                    }
                    div { class: "setting-row",
                        button { class: "btn btn-danger", onclick: move |_| { state.settings_open.set(false); state.logout(); }, "Sign out" }
                    }
                }
                div { class: "settings-section",
                    div { class: "section-title", "Appearance" }
                    div { class: "seg seg-wide",
                        button { class: if mode == ThemeMode::Light { "seg-btn active" } else { "seg-btn" }, onclick: move |_| state.set_theme_mode(ThemeMode::Light), "Light" }
                        button { class: if mode == ThemeMode::System { "seg-btn active" } else { "seg-btn" }, onclick: move |_| state.set_theme_mode(ThemeMode::System), "System" }
                        button { class: if mode == ThemeMode::Dark { "seg-btn active" } else { "seg-btn" }, onclick: move |_| state.set_theme_mode(ThemeMode::Dark), "Dark" }
                    }
                }
            }
        }
    }
}

// ---- about ----

#[component]
fn AboutModal(state: AppState) -> Element {
    let git = GITHUB_URL.to_string();
    let docs = git.clone() + "/blob/main/README.md";
    let version = VERSION.to_string();

    rsx! {
        div { class: "modal-overlay", onmousedown: move |_| state.about_open.set(false),
            div { class: "modal about", onmousedown: move |e| e.stop_propagation(),
                div { class: "modal-head",
                    div { class: "modal-title", "About OctoPort" }
                    button { class: "modal-x", title: "close", onclick: move |_| state.about_open.set(false), "✕" }
                }
                div { class: "about-body",
                    div { class: "about-logo", dangerous_inner_html: crate::logo::mark_markup() }
                    div { class: "about-name", "OctoPort" }
                    div { class: "about-ver", "version {version}" }
                    p { class: "about-desc",
                        "Open local ports to the public internet on random subdomains. Free, secure, and performant."
                    }
                    div { class: "about-links",
                        button { class: "btn btn-ghost btn-sm", onclick: move |_| open_browser(&git), "GitHub" }
                        button { class: "btn btn-ghost btn-sm", onclick: move |_| open_browser(&docs), "Documentation" }
                    }
                }
                div { class: "about-foot", "© {2026} OctoPort · Apache 2.0" }
            }
        }
    }
}

#[component]
fn AddTunnelModal(state: AppState) -> Element {
    let protocol = (state.protocol)();
    let expiry = (state.new_expiry)();
    let base_domain = (state.base_domain)();
    let base_display = if base_domain.is_empty() { "localhost".to_string() } else { base_domain.clone() };

    let active = (state.tunnels)().len();
    let max = (state.max_tunnels)();
    let quota_full = active >= max as usize;

    // Same local address + protocol already exposed? Reject before the server
    // has to. Local addrs are normalised to host:port, so compare on port too.
    let local = (state.local_addr)();
    let local_norm = if local.contains(':') { local.trim().to_string() } else { format!("localhost:{}", local.trim()) };
    let duplicate = (state.tunnels)().iter().any(|t| {
        t.protocol == protocol && {
            let existing = t.local_addr.split(':').last().unwrap_or("");
            let wanted = local_norm.split(':').last().unwrap_or("");
            !wanted.is_empty() && existing == wanted
        }
    });

    // The requested subdomain belongs to another service on the base domain
    // (portainer, octoport, ...)? The server refuses to allocate it, so block it
    // here with a clear hint before the submit round-trip.
    let requested = (state.new_subdomain)().trim().to_lowercase();
    let reserved = !requested.is_empty()
        && (state.reserved_subdomains)().iter().any(|r| r.eq_ignore_ascii_case(&requested));

    rsx! {
        div { class: "modal-overlay", onmousedown: move |_| state.new_tunnel_open.set(false),
            div { class: "modal add-tunnel", onmousedown: move |e| e.stop_propagation(),
                div { class: "modal-head",
                    div { class: "modal-title", "New tunnel" }
                    button { class: "modal-x", title: "close", onclick: move |_| state.new_tunnel_open.set(false), "✕" }
                }
                div { class: "field",
                    label { "PROTOCOL" }
                    div { class: "seg",
                        button { class: if protocol == "http" { "seg-btn active" } else { "seg-btn" }, onclick: move |_| state.protocol.set("http".into()), "HTTP" }
                        button { class: if protocol == "tcp" { "seg-btn active" } else { "seg-btn" }, onclick: move |_| state.protocol.set("tcp".into()), "TCP" }
                    }
                }
                div { class: "field",
                    label { "LOCAL ADDRESS" }
                    input {
                        r#type: "text",
                        placeholder: "3000 or 127.0.0.1:5432",
                        value: (state.local_addr)(),
                        oninput: move |e| state.local_addr.set(e.value()),
                        onkeydown: move |e| {
                            if e.code() == Code::Enter { state.submit_new_tunnel() }
                        },
                    }
                    div { class: "hint", "Port (3000) or host:port (127.0.0.1:5432) of the service to expose." }
                }
                div { class: "field",
                    label { "SUBDOMAIN (OPTIONAL)" }
                    div { class: "sub-input",
                        input {
                            r#type: "text",
                            placeholder: "my-app",
                            spellcheck: false,
                            value: (state.new_subdomain)(),
                            oninput: move |e| state.new_subdomain.set(e.value()),
                        }
                        span { class: "sub-suffix", ".{base_display}" }
                    }
                    div { class: "hint", "Leave blank for a random subdomain. Lowercase letters, digits and dashes." }
                }
                div { class: "field",
                    label { "EXPIRY" }
                    select {
                        value: "{expiry}",
                        oninput: move |e| {
                            let v: u64 = e.value().parse().unwrap_or(0);
                            state.new_expiry.set(v);
                        },
                        option { value: "0", "Default (up to 36 hours)" }
                        option { value: "1800", "30 minutes" }
                        option { value: "3600", "1 hour" }
                        option { value: "28800", "8 hours" }
                        option { value: "86400", "24 hours" }
                    }
                }
                if quota_full {
                    div { class: "hint warn", "Free plan limit reached ({active}/{max}) — close a tunnel to continue." }
                }
                if duplicate {
                    div { class: "hint warn", "This port is already exposed over {protocol}. Open a different port or protocol." }
                }
                if reserved {
                    div { class: "hint warn", "\"{requested}\" is reserved for another service on this domain." }
                }
                div { class: "modal-actions",
                    button { class: "btn btn-ghost", onclick: move |_| state.new_tunnel_open.set(false), "Cancel" }
                    button {
                        class: "btn btn-primary",
                        disabled: quota_full || duplicate || reserved,
                        onclick: move |_| state.submit_new_tunnel(),
                        if quota_full { "Limit reached" } else { "Expose" }
                    }
                }
            }
        }
    }
}

// ---- tunnels ----

#[component]
fn TunnelsTab(state: AppState) -> Element {
    let tunnels = (state.tunnels)();
    let count = tunnels.len();

    rsx! {
        div { class: "panel-head",
            h2 { class: "panel-title", "Tunnels" }
            div { class: "panel-count", "{count} active" }
        }
        if tunnels.is_empty() {
            div { class: "empty empty-cta",
                div { class: "empty-icon", "↗" }
                h3 { "No live tunnels" }
                p { "Open a local port and share it with the world." }
                button { class: "btn btn-primary", onclick: move |_| state.new_tunnel_open.set(true), "+ Create New Tunnel" }
            }
        } else {
            for t in tunnels.iter() {
                TunnelCard { key: "{t.id}", state, tunnel: t.clone() }
            }
        }
    }
}

#[component]
fn TunnelCard(state: AppState, tunnel: TunnelListItem) -> Element {
    // Cards always render expanded (the detail grid never collapses on click),
    // so rows never shrink — quota caps tunnels at 5 so this stays compact.
    let remaining = remaining(&tunnel.expires_at).unwrap_or_default();
    let url = tunnel.url.clone();
    let url_copy = url.clone();
    let url_href = url.clone();
    let id = tunnel.id.clone();
    let id_toggle = id.clone();
    let id_delete = id.clone();
    let is_bound = tunnel.bound;
    let enabled = tunnel.enabled;
    let protocol = tunnel.protocol.clone().to_uppercase();
    let reqs = format_count(tunnel.requests);
    let last_active = fmt_last_active(tunnel.last_active_at);
    let card_cls = if !enabled { "card paused" } else { "card open" };

    rsx! {
        div {
            class: card_cls,
            div { class: "card-status",
                if !enabled {
                    div { class: "paused-badge", "paused" }
                } else if is_bound {
                    div { class: "live-badge", "live" }
                } else {
                    div { class: "wait-badge", "waiting" }
                }
            }
            div { class: "card-main",
                div { class: "card-url", {tunnel.url.clone()} }
                div { class: "card-sub",
                    span { class: "proto", {protocol.clone()} }
                    span { class: "local", "→ {tunnel.local_addr}" }
                }
            }
            div { class: "card-meta",
                div { class: "reqs", {reqs.clone()} " req" }
                div { class: "ends", "ends in {remaining}" }
            }
            div { class: "card-toggle",
                div { class: if enabled { "switch on" } else { "switch" },
                    title: if enabled { "Pause (keeps subdomain)" } else { "Resume" },
                    onclick: move |e| { e.stop_propagation(); state.toggle_tunnel(&id_toggle, !enabled); },
                    span { class: "switch-knob" }
                }
            }
            button {
                class: "icon-btn",
                title: "copy URL",
                onclick: move |e| {
                    e.stop_propagation();
                    copy_text(&url);
                    state.toast(ToastKind::Info, format!("copied {url}"));
                },
                svg { view_box: "0 0 24 24", fill: "none", stroke: "currentColor", stroke_width: "2", stroke_linecap: "round", stroke_linejoin: "round",
                    rect { x: "9", y: "9", width: "13", height: "13", rx: "2" }
                    path { d: "M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" }
                }
            }
            button {
                class: "icon-btn danger",
                title: "delete tunnel",
                onclick: move |e| {
                    e.stop_propagation();
                    state.delete_tunnel(&id_delete);
                },
                svg { view_box: "0 0 24 24", fill: "none", stroke: "currentColor", stroke_width: "2", stroke_linecap: "round", stroke_linejoin: "round",
                    path { d: "M3 6h18" }
                    path { d: "M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" }
                    path { d: "M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" }
                    path { d: "M10 11v6" }
                    path { d: "M14 11v6" }
                }
            }
            div { class: "card-detail",
                div { class: "detail-grid",
                    Detail { label: "Public URL", value: url.clone(), mono: true }
                    Detail { label: "Local", value: tunnel.local_addr.clone(), mono: true }
                    Detail { label: "Protocol", value: protocol.clone(), mono: false }
                    Detail { label: "Requests", value: reqs.clone(), mono: false }
                    Detail { label: "Data in", value: human_bytes(tunnel.bytes_in), mono: true }
                    Detail { label: "Data out", value: human_bytes(tunnel.bytes_out), mono: true }
                    Detail { label: "Last active", value: last_active.clone(), mono: false }
                    Detail { label: "Ends in", value: remaining.clone(), mono: false }
                }
                div { class: "card-open-url",
                    a { href: url_href.clone(), onclick: |e| e.prevent_default(), "Open in browser" }
                    button { class: "btn btn-ghost btn-sm", onclick: move |e| { e.stop_propagation(); copy_text(&url_copy); }, "Copy URL" }
                }
            }
        }
    }
}

#[component]
fn Detail(label: String, value: String, mono: bool) -> Element {
    rsx! {
        div { class: "detail-item",
            div { class: "detail-label", {label} }
            div { class: if mono { "detail-value mono" } else { "detail-value" }, {value} }
        }
    }
}

// ---- usage ----

#[component]
fn UsageTab(state: AppState) -> Element {
    let tunnels = (state.tunnels)();
    let base_domain = (state.base_domain)();
    let base_display = if base_domain.is_empty() { "localhost".to_string() } else { base_domain.clone() };

    let mut total_req = 0u64;
    let mut total_in = 0u64;
    let mut total_out = 0u64;
    for t in &tunnels {
        total_req += t.requests;
        total_in += t.bytes_in;
        total_out += t.bytes_out;
    }

    let max = tunnels.iter().map(|t| t.requests.max(1)).max().unwrap_or(1).max(1);
    let max_in = tunnels.iter().map(|t| t.bytes_in.max(1)).max().unwrap_or(1).max(1);
    let max_out = tunnels.iter().map(|t| t.bytes_out.max(1)).max().unwrap_or(1).max(1);

    rsx! {
        div { class: "panel-head",
            h2 { class: "panel-title", "Usage" }
        }
        RealtimeChart { state }
        div { class: "stats-row",
            div { class: "stat-card", div { class: "num", "{tunnels.len()}" } div { class: "lbl", "active tunnels" } }
            div { class: "stat-card", div { class: "num", "{total_req}" } div { class: "lbl", "requests" } }
            div { class: "stat-card", div { class: "num", "{human_bytes(total_in)}" } div { class: "lbl", "received" } }
            div { class: "stat-card", div { class: "num", "{human_bytes(total_out)}" } div { class: "lbl", "sent" } }
        }
        if tunnels.is_empty() {
            div { class: "empty",
                div { class: "empty-icon", "▤" }
                h3 { "No tunnels yet" }
                p { "Expose a local service to see usage stats." }
            }
        } else {
            div { class: "usage-table",
                div { class: "usage-head",
                    span { "Tunnel" }
                    span { "Requests" }
                    span { "In" }
                    span { "Out" }
                    span { "" }
                }
                for t in tunnels.iter() {
                    UsageRow { state, key: "{t.id}", tunnel: t.clone(), max, max_in, max_out, base_display: base_display.clone() }
                }
            }
        }
    }
}

#[component]
fn UsageRow(
    state: AppState,
    tunnel: TunnelListItem,
    max: u64,
    max_in: u64,
    max_out: u64,
    base_display: String,
) -> Element {
    let sub = tunnel.subdomain.clone();
    let color = series_color_for(&sub).to_string();

    rsx! {
        div {
            class: "usage-row",
            div { class: "usage-cell name",
                div { class: "usage-sub",
                    span { class: "legend-dot", style: "background: {color}" }
                    "{tunnel.subdomain}.{base_display}"
                }
                div { class: "usage-local", "{tunnel.protocol} → {tunnel.local_addr}" }
            }
            div { class: "usage-cell",
                div { class: "mini-label", "{tunnel.requests}" }
                div { class: "mini-bar", div { class: "mini-fill", style: "width: {pct_width(tunnel.requests, max)}" } }
            }
            div { class: "usage-cell",
                div { class: "mini-label", "{human_bytes(tunnel.bytes_in)}" }
                div { class: "mini-bar", div { class: "mini-fill in", style: "width: {pct_width(tunnel.bytes_in, max_in)}" } }
            }
            div { class: "usage-cell",
                div { class: "mini-label", "{human_bytes(tunnel.bytes_out)}" }
                div { class: "mini-bar", div { class: "mini-fill out", style: "width: {pct_width(tunnel.bytes_out, max_out)}" } }
            }
            div { class: "usage-cell actions",
                button {
                    class: "insight-btn",
                    title: "Open this tunnel's insight window",
                    onclick: move |e| { e.stop_propagation(); state.open_insight(&sub); },
                    "details"
                }
            }
        }
    }
}

/// The aggregate real-time AREA chart: every tunnel on one shared axes, each
/// with its own colour and gradient fill, plus a legend below. There is
/// deliberately no scope dropdown — the aggregate view IS all the tunnels at
/// once.
///
/// This component only emits the container div. The chart engine's
/// MutationObserver mounts a React root into it, and the unified push loop in
/// `App` feeds it live data every 50ms, so the right edge of each curve keeps
/// chasing the SSE target every frame instead of freezing for the tick
/// interval.
#[component]
fn RealtimeChart(state: AppState) -> Element {
    let base_domain = (state.base_domain)();
    let base_display = if base_domain.is_empty() { "localhost".to_string() } else { base_domain.clone() };
    let tunnels = (state.tunnels)();

    // Stable initial payload, built from the tunnel set only so it never
    // churns at 60fps and Dioxus never replaces the container node underneath
    // the React root. The engine mounts on this; the push loop in `App` takes
    // over within one frame. When tunnels are added or removed the payload
    // changes and the engine remounts with the right series count, seeded from
    // the last pushed payload so it doesn't flash back to an empty frame.
    let init = chart_container_html("realtime-chart", &tunnels, &base_display);

    rsx! {
        div { class: "chart-card",
            div { class: "chart-head",
                div { class: "chart-title-wrap",
                    span { class: "chart-title", "Realtime activity" }
                    span { class: "chart-scope-name", "all tunnels · live" }
                }
            }
            // Always render the chart div, even with no tunnels, so the
            // engine mounts it and the push loop has a target from the start.
            div { dangerous_inner_html: init }
            if !tunnels.is_empty() {
                ChartLegend { state, base_display }
            }
        }
    }
}

/// Smooth a rolling series so the plotted curve is soft rather than a
/// sawtooth of per-second request counts.
///
/// A 3-tap weighted kernel (0.22 / 0.56 / 0.22) with edge clamping. This is
/// deliberately mild: enough to round off the corners between adjacent
/// samples while still preserving the height of a genuine one-second spike.
/// Recharts then draws the result with a `monotone` cubic, which interpolates
/// smoothly between the smoothed points without overshooting them.
///
/// `None` entries are the left-hand padding of a not-yet-full window and are
/// left untouched — they must stay gaps, never become zeros.
/// Centre weight of the smoothing kernel. The two neighbours split the
/// remainder equally, so 1.0 disables smoothing entirely and lower values
/// smooth harder. Note the trade-off: an isolated one-second spike is damped
/// to roughly this fraction of its true height, so don't push it much below
/// ~0.5 if peak request rates need to read accurately off the y-axis.
const SMOOTH_CENTER: f32 = 0.56;

fn smooth_series(vals: &[Option<f32>]) -> Vec<Option<f32>> {
    let side = (1.0 - SMOOTH_CENTER) / 2.0;
    let n = vals.len();
    let mut out = Vec::with_capacity(n);
    for i in 0..n {
        let Some(cur) = vals[i] else {
            out.push(None);
            continue;
        };
        // Clamp at the edges of the *real* (non-None) span so smoothing never
        // drags a value toward a gap.
        let prev = if i > 0 { vals[i - 1].unwrap_or(cur) } else { cur };
        let next = if i + 1 < n { vals[i + 1].unwrap_or(cur) } else { cur };
        out.push(Some(prev * side + cur * SMOOTH_CENTER + next * side));
    }
    out
}

/// Build the aggregate (or single-tunnel) realtime area-chart payload.
///
/// The window is a FIXED `CHART_WINDOW` slots wide and right-anchored: the
/// newest sample always sits at the right edge under the "now" label, and
/// history extends leftwards. Slots with no data yet are `null`, not `0`.
///
/// That fixed, right-anchored, null-padded shape is what fixes the three
/// x-axis complaints at once:
///   * the axis no longer rescales on every tick (it did when the label count
///     grew with the data), so the curve glides instead of jumping;
///   * a fresh tunnel no longer paints a flat zero line back across negative
///     time, implying traffic before it existed — those slots are gaps;
///   * a tunnel that starts later than another on the same chart stays
///     aligned to "now" instead of sliding to the wrong end of the axis.
///
/// `only` restricts the payload to a single subdomain (the insight modal).
fn realtime_payload(state: &AppState, only: Option<&str>) -> serde_json::Value {
    let tunnels = (state.tunnels)();
    let base_domain = (state.base_domain)();
    let base_display =
        if base_domain.is_empty() { "localhost".to_string() } else { base_domain.clone() };
    let anim = (state.chart_anim)();
    let tips = (state.chart_tip)();

    let mut series: Vec<Vec<Option<f32>>> = Vec::new();
    let mut names: Vec<String> = Vec::new();
    let mut colors: Vec<String> = Vec::new();

    for t in tunnels.iter().filter(|t| only.map_or(true, |s| t.subdomain == s)) {
        let q = anim.get(&t.subdomain).cloned().unwrap_or_default();
        let tip = tips.get(&t.subdomain).copied().unwrap_or((0.0, 0.0, 1.0));
        let tip_val = tip.0 + (tip.1 - tip.0) * ease_in_out(tip.2);

        // Committed history (oldest..newest) plus the still-easing tip as the
        // newest point on the right.
        let mut vals: Vec<Option<f32>> = q.iter().copied().map(Some).collect();
        vals.push(Some(tip_val));

        // Right-anchor into the fixed window: trim from the left if we somehow
        // overflow, pad with `None` (a gap) if the window isn't full yet.
        if vals.len() > CHART_WINDOW {
            vals.drain(0..vals.len() - CHART_WINDOW);
        } else if vals.len() < CHART_WINDOW {
            let pad = CHART_WINDOW - vals.len();
            vals.splice(0..0, std::iter::repeat_n(None, pad));
        }

        series.push(smooth_series(&vals));
        names.push(format!("{}.{}", t.subdomain, base_display));
        colors.push(series_color_for(&t.subdomain).to_string());
    }

    // "empty" tells the chart engine to draw a "No data" state instead of an
    // axis with a flat line pinned to zero. A series of all-nulls or all-zeros
    // carries no information, and rendering it as a chart invites the reader
    // to interpret a baseline as real measured traffic.
    let empty = series
        .iter()
        .all(|vals| vals.iter().all(|v| v.map_or(true, |x| x <= f32::EPSILON)));

    serde_json::json!({
        "values": series,
        "labels": window_labels(),
        "ticks": window_ticks(),
        "names": names,
        "colors": colors,
        "empty": empty,
        "emptyText": if series.is_empty() { "No tunnels" } else { "No data" },
    })
}

/// X labels for the fixed realtime window.
///
/// The control plane emits one "stats" frame per second, so slot `i` is
/// `CHART_WINDOW - 1 - i` seconds in the past and the last slot is "now".
///
/// Labels are phrased as elapsed time ("1m 59s ago"), never as a negative
/// coordinate. The old "-119s" form read as though the chart were plotting
/// into negative time, which is meaningless to a viewer and looked like a
/// rendering fault rather than a time axis.
fn window_labels() -> Vec<String> {
    (0..CHART_WINDOW).map(|i| ago_label(CHART_WINDOW - 1 - i)).collect()
}

/// Human phrasing for "n seconds in the past".
fn ago_label(ago: usize) -> String {
    if ago == 0 {
        return "now".to_string();
    }
    if ago < 60 {
        return format!("{ago}s ago");
    }
    let (m, sec) = (ago / 60, ago % 60);
    if sec == 0 { format!("{m}m ago") } else { format!("{m}m {sec}s ago") }
}

/// The subset of labels that actually get drawn under the axis.
///
/// Recharts is told to render only these, so the axis stays readable instead
/// of thinning 120 labels by an interval rule that produces arbitrary values.
fn window_ticks() -> Vec<String> {
    const SLOTS: usize = 4;
    let step = CHART_WINDOW / SLOTS;
    let mut ticks: Vec<String> = (0..SLOTS).map(|k| ago_label(CHART_WINDOW - 1 - k * step)).collect();
    ticks.push("now".to_string());
    ticks.dedup();
    ticks
}

/// Escape a string for use inside a double-quoted HTML attribute. JSON payloads
/// embedded in `data-*` attributes contain `"` characters that would otherwise
/// terminate the attribute early and corrupt the DOM.
fn esc_attr(s: &str) -> String {
    s.replace('&', "&amp;").replace('"', "&quot;").replace('<', "&lt;").replace('>', "&gt;")
}

/// Emit the container div for a chart as raw HTML so the React root can own the
/// element's children without Dioxus diffing them away.
///
/// `payload` is the initial `{values, labels, names, colors}` object; the push
/// loop takes over within one frame. Keeping this string STABLE across
/// re-renders matters: if it changes, Dioxus replaces the node and the engine
/// has to tear down and re-mount the React root.
fn chart_div_html(id: &str, name: &str, payload: &serde_json::Value) -> String {
    let nil = serde_json::json!([]);
    let field = |k: &str| payload.get(k).unwrap_or(&nil).to_string();
    format!(
        r#"<div id="{id}" class="rust-chart" data-name="{name}" data-chart-values="{v}" data-chart-labels="{l}" data-chart-ticks="{tk}" data-chart-series-names="{n}" data-chart-colors="{c}" data-chart-empty="{e}" data-chart-empty-text="{et}"></div>"#,
        v = esc_attr(&field("values")),
        l = esc_attr(&field("labels")),
        tk = esc_attr(&field("ticks")),
        n = esc_attr(&field("names")),
        c = esc_attr(&field("colors")),
        e = esc_attr(&field("empty")),
        et = esc_attr(payload.get("emptyText").and_then(|v| v.as_str()).unwrap_or("No data")),
    )
}

/// The aggregate realtime chart's initial payload: the full fixed window of
/// labels with an all-`null` series per tunnel.
///
/// Emitting the labels (rather than an empty array, as a previous iteration
/// did) gives Recharts a domain to lay the axes and grid out from on the very
/// first paint, so the chart frame appears immediately and the curve then
/// grows into it — instead of rendering literally nothing until data lands.
/// The series are all-`null` so no fake zero baseline is drawn.
fn chart_container_html(id: &str, tunnels: &[TunnelListItem], base_display: &str) -> String {
    let series: Vec<Vec<Option<f32>>> =
        tunnels.iter().map(|_| vec![None; CHART_WINDOW]).collect();
    let names: Vec<String> =
        tunnels.iter().map(|t| format!("{}.{}", t.subdomain, base_display)).collect();
    let colors: Vec<String> =
        tunnels.iter().map(|t| series_color_for(&t.subdomain).to_string()).collect();
    let payload = serde_json::json!({
        "values": series,
        "labels": window_labels(),
        "ticks": window_ticks(),
        "names": names,
        "colors": colors,
        "empty": true,
        "emptyText": if tunnels.is_empty() { "No tunnels" } else { "No data" },
    });
    chart_div_html(id, "AreaChart", &payload)
}

/// Initial payload for the insight modal's per-tunnel realtime area chart.
fn insight_area_html(tunnel: &TunnelListItem, base_display: &str) -> String {
    let payload = serde_json::json!({
        "values": [vec![None::<f32>; CHART_WINDOW]],
        "labels": window_labels(),
        "ticks": window_ticks(),
        "names": [format!("{}.{}", tunnel.subdomain, base_display)],
        "colors": [series_color_for(&tunnel.subdomain)],
        "empty": true,
        "emptyText": "No data",
    });
    chart_div_html("insight-area", "AreaChart", &payload)
}

/// Initial payload for the insight modal's in-vs-out pie chart. Non-zero
/// placeholder values avoid NaN arc geometry from 0/0 before data arrives.
fn insight_pie_html() -> String {
    let payload = serde_json::json!({
        "values": [50.0, 50.0],
        "labels": ["In", "Out"],
        "names": [],
        "colors": [],
        "empty": true,
        "emptyText": "No data",
    });
    chart_div_html("insight-pie", "PieChart", &payload)
}

/// Initial payload for the insight modal's health radar.
fn insight_radar_html() -> String {
    let payload = serde_json::json!({
        "values": [0.0, 0.0, 0.0, 0.0, 0.0],
        "labels": ["Uptime", "Requests", "In", "Out", "Active"],
        "names": ["Health"],
        "colors": [],
        "empty": true,
        "emptyText": "No data",
    });
    chart_div_html("insight-radar", "RadarChart", &payload)
}

/// Initial payload for the insight modal's availability gauge.
fn insight_radial_html() -> String {
    let payload = serde_json::json!({
        "values": [50.0, 50.0],
        "labels": ["Availability", "Load"],
        "names": [],
        "colors": [],
    });
    chart_div_html("insight-radial", "RadialChart", &payload)
}

/// Realtime decayed "how active was this tunnel recently" 0–100 score.
fn activity_pct(last_active: i64) -> f32 {
    if last_active <= 0 {
        return 0.0;
    }
    let now = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs() as i64).unwrap_or(0);
    let age = (now - last_active).max(0) as f32;
    (100.0 / (1.0 + age / 120.0)).clamp(0.0, 100.0)
}

/// Serialize the insight modal's data-split pie (share of this tunnel's bytes).
fn pie_live(state: &AppState, sub: &str) -> Option<serde_json::Value> {
    let tunnels = (state.tunnels)();
    let t = tunnels.iter().find(|t| t.subdomain == sub)?;
    let total = t.bytes_in + t.bytes_out;
    let (in_pct, out_pct) = if total == 0 {
        (0.0, 0.0)
    } else {
        ((t.bytes_in as f32 / total as f32) * 100.0, (t.bytes_out as f32 / total as f32) * 100.0)
    };
    Some(serde_json::json!({
        "values": [in_pct, out_pct],
        "labels": ["In", "Out"],
        "names": [],
        "colors": [],
        // A 50/50 ring drawn for a tunnel that has moved zero bytes is a lie;
        // say so explicitly instead.
        "empty": total == 0,
        "emptyText": "No data",
    }))
}

/// Serialize the insight modal's 5-axis health radar (0–100 each).
fn radar_live(state: &AppState, sub: &str) -> Option<serde_json::Value> {
    let tunnels = (state.tunnels)();
    let t = tunnels.iter().find(|t| t.subdomain == sub)?;
    let max_req = tunnels.iter().map(|x| x.requests.max(1)).max().unwrap_or(1);
    let max_in = tunnels.iter().map(|x| x.bytes_in.max(1)).max().unwrap_or(1);
    let max_out = tunnels.iter().map(|x| x.bytes_out.max(1)).max().unwrap_or(1);
    let uptime = if t.bound && t.enabled { 100.0 } else { 0.0 };
    let reqs = (t.requests as f32 / max_req as f32 * 100.0).clamp(0.0, 100.0);
    let inflow = (t.bytes_in as f32 / max_in as f32 * 100.0).clamp(0.0, 100.0);
    let outflow = (t.bytes_out as f32 / max_out as f32 * 100.0).clamp(0.0, 100.0);
    let active = activity_pct(t.last_active_at);
    Some(serde_json::json!({
        "values": [uptime, reqs, inflow, outflow, active],
        "labels": ["Uptime", "Requests", "In", "Out", "Active"],
        "names": ["Health"],
        "colors": [series_color_for(sub)],
        "empty": uptime <= 0.0 && reqs <= 0.0 && inflow <= 0.0 && outflow <= 0.0 && active <= 0.0,
        "emptyText": "No data",
    }))
}

/// Serialize the insight modal's availability gauges (0–100 each): uptime and
/// current request-rate load relative to the busiest tunnel.
fn radial_live(state: &AppState, sub: &str) -> Option<serde_json::Value> {
    let tunnels = (state.tunnels)();
    let t = tunnels.iter().find(|t| t.subdomain == sub)?;
    let availability = if t.bound && t.enabled { 100.0 } else { 0.0 };
    let metrics = (state.tunnel_metrics)();
    let rate = metrics.get(sub).and_then(|q| q.back()).copied().unwrap_or(0) as f32;
    let peak = tunnels
        .iter()
        .filter_map(|x| metrics.get(&x.subdomain).and_then(|q| q.back()))
        .copied()
        .max()
        .unwrap_or(1)
        .max(1) as f32;
    let load = (rate / peak * 100.0).clamp(0.0, 100.0);
    Some(serde_json::json!({
        // Never flagged empty: availability is meaningful (and worth showing)
        // even when current load is zero.
        "values": [availability, load],
        "labels": ["Availability", "Load"],
        "names": [],
        "colors": [],
        "empty": false,
    }))
}

#[component]
fn ChartLegend(state: AppState, base_display: String) -> Element {
    let tunnels = (state.tunnels)();
    let base_display = base_display.clone();
    let chips: Vec<(String, String, String)> = tunnels
        .iter()
        .map(|t| {
            (
                t.subdomain.clone(),
                series_color_for(&t.subdomain).to_string(),
                format!("{}.{}", t.subdomain, base_display),
            )
        })
        .collect();

    rsx! {
        div { class: "chart-legend",
            {chips.iter().map(|(sub, color, label)| {
                let sub = sub.clone();
                let color = color.clone();
                let label = label.clone();
    #[cfg(not(target_os = "linux"))]
    {
    // ---- system tray ----
    // Build the tray once; the menu is swapped wholesale whenever tunnels or
    // auth change (see below), because muda menus can't be incrementally
    // cleared. The tray keeps running after the window hides to the tray.
    let tray = use_hook(move || {
        let icon = icon_from_memory::<DioxusTrayIcon>(
            include_bytes!("../assets/octoport-tray-icon.png"),
        )
        .ok();
        init_tray_icon(build_tray_menu(&state), icon)
    });

    // Route tray menu clicks into the app state.
    let win = use_window();
    let tray_win = win.clone();
    use_tray_menu_event_handler(move |event| {
        let id = event.id().as_ref().to_string();
        handle_tray_event(state, tray_win.clone(), &id);
    });

    // Keep the tray menu in sync with the tunnels and auth state.
    let tray_sync = tray.clone();
    use_effect(move || {
        let _ = (state.tunnels)();
        let _ = (state.auth)();
        tray_sync.set_menu(Some(Box::new(build_tray_menu(&state))));
    });
    }

    rsx! {
                    div { class: "legend-chip", title: "Click to open insight",
                        onclick: move |_| state.open_insight(&sub),
                        span { class: "legend-dot", style: "background: {color}" }
                        span { class: "legend-name", "{label}" }
                    }
                }
            })}
        }
    }
}

/// Stable per-subdomain color so a tunnel keeps its hue across re-renders.
fn series_color_for(sub: &str) -> &'static str {
    let mut h: u32 = 2166136261;
    for b in sub.bytes() {
        h ^= b as u32;
        h = h.wrapping_mul(16777619);
    }
    const PALETTE: [&str; 8] = [
        "#CCCCFF", "#6EE7B7", "#FBBF24", "#F472B6",
        "#60A5FA", "#A78BFA", "#FB923C", "#34D399",
    ];
    PALETTE[(h as usize) % PALETTE.len()]
}

/// Ease a 0→1 progress value with a smooth ease-in-out curve.
fn ease_in_out(t: f32) -> f32 {
    let t = t.clamp(0.0, 1.0);
    t * t * (3.0 - 2.0 * t)
}

// ---- tunnel insight ----

/// Floating window for a single tunnel (like Settings): a live realtime chart,
/// richer metrics, an on/off pause switch, and a rolling log of recent events.
/// Opens when a usage row or legend chip is clicked.
#[component]
fn InsightModal(state: AppState) -> Element {
    let sub = (state.insight_sub)();
    let tunnels = (state.tunnels)();
    let tunnel = tunnels.iter().find(|t| t.subdomain == sub).cloned();
    let Some(tunnel) = tunnel else {
        // Tunnel disappeared while the window was open — close it.
        spawn_forever(async move {
            tokio::time::sleep(Duration::from_millis(200)).await;
            state.close_insight();
        });
        return rsx! { div { class: "modal-overlay", onmousedown: move |_| state.close_insight() } };
    };

    // Keep the event log fresh while the window is open.
    {
        let api_url = AppState::settings().api_url;
        let auth = (state.auth)();
        let token = auth.map(|a| a.token.clone());
        let events = state.insight_events;
        let sub_clone = sub.clone();
        use_effect(move || {
            let Some(token) = token.clone() else { return };
            let api_url = api_url.clone();
            let sub_clone = sub_clone.clone();
            let mut events = events;
            spawn(async move {
                let api = api_url.clone();
                let mut fail_streak: u32 = 0;
                loop {
                    match Client::new(api.clone())
                        .with_token(&token.clone())
                        .list_events(200)
                        .await
                    {
                        Ok(list) => {
                            fail_streak = 0;
                            let mine: Vec<EventItem> = list
                                .into_iter()
                                .filter(|e| {
                                    e.payload.get("subdomain").and_then(|v| v.as_str())
                                        == Some(&sub_clone)
                                })
                                .take(60)
                                .collect();
                            if (events)().as_slice() != mine.as_slice() {
                                events.set(mine);
                            }
                        }
                        Err(_) => {
                            // Back off rather than retrying a failing endpoint
                            // on a fixed short interval.
                            fail_streak = fail_streak.saturating_add(1).min(4);
                        }
                    }
                    let wait = INSIGHT_EVENTS_POLL_SECS * (1u64 << fail_streak);
                    tokio::time::sleep(Duration::from_secs(wait)).await;
                }
            });
        });
    }

    let base_domain = (state.base_domain)();
    let base_display = if base_domain.is_empty() { "localhost".to_string() } else { base_domain.clone() };
    let color = series_color_for(&sub).to_string();

    // Four charts: a per-tunnel realtime area chart, an in-vs-out pie, a
    // 5-axis health radar and an availability gauge. Each div's initial
    // payload is a static placeholder, stable across re-renders so Dioxus
    // never replaces the container out from under the React root. The unified
    // push loop in `App` feeds them live data while this modal is open.
    let area_html = insight_area_html(&tunnel, &base_display);
    let pie_html = insight_pie_html();
    let radar_html = insight_radar_html();
    let radial_html = insight_radial_html();

    // NOTE: there is deliberately no push loop here. All four of this modal's
    // charts are fed by the single unified loop in `App`, which watches
    // `insight_sub` and includes them in its batch while the modal is open.
    // Per-component loops used to be spawned from `use_effect` and were never
    // cancelled on remount, so switching tabs or reopening the modal stacked
    // up duplicate loops all pushing to the same chart ids.

    let events = (state.insight_events)();
    let is_bound = tunnel.bound;
    let enabled = tunnel.enabled;
    let reqs = format_count(tunnel.requests);
    let remaining = remaining(&tunnel.expires_at).unwrap_or_default();
    let last_active = fmt_last_active(tunnel.last_active_at);
    let id = tunnel.id.clone();
    let id_close = tunnel.id.clone();

    rsx! {
        div { class: "modal-overlay", onmousedown: move |_| state.close_insight(),
            div { class: "modal insight", onmousedown: move |e| e.stop_propagation(),
                div { class: "modal-head",
                    div { class: "modal-title",
                        span { class: "legend-dot", style: "background: {color}" }
                        "{tunnel.subdomain}.{base_display}"
                    }
                    button { class: "modal-x", title: "close", onclick: move |_| state.close_insight(), "✕" }
                }
                div { class: "insight-status",
                    if is_bound && enabled {
                        div { class: "live-badge", "live" }
                    } else if !enabled {
                        div { class: "paused-badge", "paused" }
                    } else {
                        div { class: "wait-badge", "waiting" }
                    }
                    div { class: "insight-url", "{tunnel.protocol} → {tunnel.local_addr}" }
                }
                div { class: "insight-charts",
                    div { class: "insight-chart",
                        div { class: "insight-chart-title", "Realtime rate" }
                        div { dangerous_inner_html: area_html }
                    }
                    div { class: "insight-chart-grid",
                        div { class: "insight-chart-card",
                            div { class: "insight-chart-title", "Data split" }
                            div { dangerous_inner_html: pie_html }
                        }
                        div { class: "insight-chart-card",
                            div { class: "insight-chart-title", "Health" }
                            div { dangerous_inner_html: radar_html }
                        }
                        div { class: "insight-chart-card",
                            div { class: "insight-chart-title", "Availability" }
                            div { dangerous_inner_html: radial_html }
                        }
                    }
                }
                div { class: "insight-metrics",
                    div { class: "metric", div { class: "metric-lbl", "REQUESTS" } div { class: "metric-val", "{reqs}" } }
                    div { class: "metric", div { class: "metric-lbl", "DATA IN" } div { class: "metric-val", "{human_bytes(tunnel.bytes_in)}" } }
                    div { class: "metric", div { class: "metric-lbl", "DATA OUT" } div { class: "metric-val", "{human_bytes(tunnel.bytes_out)}" } }
                    div { class: "metric", div { class: "metric-lbl", "LAST ACTIVE" } div { class: "metric-val", "{last_active}" } }
                    div { class: "metric", div { class: "metric-lbl", "ENDS IN" } div { class: "metric-val", "{remaining}" } }
                    div { class: "metric",
                        div { class: "metric-lbl", "TUNNEL" }
                        div { class: "metric-val toggle-wrap",
                            div { class: if enabled { "switch on" } else { "switch" },
                                onclick: move |e| { e.stop_propagation(); state.toggle_tunnel(&id, !enabled); },
                                span { class: "switch-knob" }
                            }
                            span { class: "switch-lbl", {if enabled { "On" } else { "Paused" }} }
                        }
                    }
                }
                div { class: "insight-log",
                    div { class: "section-title", "Recent activity" }
                    if events.is_empty() {
                        div { class: "insight-log-empty", "No events yet — activity will appear here as traffic flows." }
                    } else {
                        for e in events.iter() {
                            div { class: "log-row",
                                span { class: "log-time", "{fmt_event_time(&e.created_at)}" }
                                span { class: "log-kind", "{e.kind}" }
                                span { class: "log-payload", "{fmt_event_payload(&e.payload)}" }
                            }
                        }
                    }
                }
                div { class: "modal-actions",
                    button { class: "btn btn-ghost", onclick: move |_| state.close_insight(), "Close" }
                    button { class: "btn btn-danger", onclick: move |_| { state.close_insight(); state.delete_tunnel(&id_close); }, "Delete tunnel" }
                }
            }
        }
    }
}

fn fmt_event_time(created_at: &str) -> String {
    if let Some(ts) = parse_rfc3339(created_at) {
        let now = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs() as i64).unwrap_or(0);
        let age = (now - ts).max(0);
        if age < 60 {
            return format!("{age}s");
        }
        if age < 3600 {
            return format!("{}m", age / 60);
        }
        if age < 86400 {
            return format!("{}h", age / 3600);
        }
        return format!("{}d", age / 86400);
    }
    created_at.to_string()
}

fn fmt_event_payload(payload: &serde_json::Value) -> String {
    let mut parts = Vec::new();
    if let Some(v) = payload.get("protocol").and_then(|v| v.as_str()) {
        parts.push(v.to_string());
    }
    if let Some(v) = payload.get("local_addr").and_then(|v| v.as_str()) {
        parts.push(v.to_string());
    }
    if parts.is_empty() {
        String::new()
    } else {
        parts.join(" · ")
    }
}

// ---- toasts ----

#[component]
fn ToastStack(state: AppState) -> Element {
    let toasts = (state.toasts)();

    rsx! {
        div { class: "toasts",
            for t in toasts.iter() {
                div { class: "toast {toast_class(t.kind)}", key: "{t.id}",
                    span { class: "ic", "{toast_icon(t.kind)}" }
                    span { {t.text.clone()} }
                }
            }
        }
    }
}

fn toast_icon(kind: ToastKind) -> &'static str {
    match kind {
        ToastKind::Success => "✓",
        ToastKind::Error => "✕",
        ToastKind::Info => "ℹ",
    }
}

fn toast_class(kind: ToastKind) -> &'static str {
    match kind {
        ToastKind::Success => "success",
        ToastKind::Error => "error",
        ToastKind::Info => "info",
    }
}

// ---- helpers ----

/// Open the platform default browser at `url` without blocking the UI thread.
#[cfg(target_os = "macos")]
fn open_browser(url: &str) {
    let _ = std::process::Command::new("open").arg(url).spawn();
}

#[cfg(target_os = "windows")]
fn open_browser(url: &str) {
    let _ = std::process::Command::new("cmd").args(["/C", "start", "", url]).spawn();
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
fn open_browser(url: &str) {
    let _ = std::process::Command::new("xdg-open").arg(url).spawn();
}

fn copy_text(text: &str) {
    let escaped = serde_json::to_string(text).unwrap_or_else(|_| "\"\"".into());
    let js = format!(
        r#"navigator.clipboard.writeText({escaped}).catch(function() {{
            const ta = document.createElement('textarea');
            ta.value = {escaped};
            document.body.appendChild(ta);
            ta.select();
            document.execCommand('copy');
            document.body.removeChild(ta);
        }});"#
    );
    let _ = eval(&js);
}

fn pct_width(requests: u64, max: u64) -> String {
    let pct = (requests as f32 / max as f32) * 100.0;
    format!("{:.1}%", pct.clamp(0.0, 100.0))
}

fn format_count(n: u64) -> String {
    if n >= 1_000_000 {
        format!("{:.1}M", n as f64 / 1_000_000.0)
    } else if n >= 1_000 {
        format!("{:.1}k", n as f64 / 1_000.0)
    } else {
        format!("{n}")
    }
}

fn remaining(expires_at: &str) -> Option<String> {
    let deadline = parse_rfc3339(expires_at)?;
    let now = SystemTime::now().duration_since(UNIX_EPOCH).ok()?.as_secs() as i64;
    let left = deadline - now;
    if left <= 0 {
        return Some("expired".into());
    }
    let days = left / 86400;
    let hours = (left % 86400) / 3600;
    let mins = (left % 3600) / 60;
    if days > 0 {
        Some(format!("{days}d {hours}h"))
    } else if hours > 0 {
        Some(format!("{hours}h {mins}m"))
    } else {
        Some(format!("{mins}m"))
    }
}

fn fmt_last_active(ts: i64) -> String {
    if ts <= 0 {
        return "never".into();
    }
    let now = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs() as i64).unwrap_or(0);
    let age = (now - ts).max(0);
    if age < 60 {
        format!("{age}s ago")
    } else if age < 3600 {
        format!("{}m ago", age / 60)
    } else if age < 86400 {
        format!("{}h ago", age / 3600)
    } else {
        format!("{}d ago", age / 86400)
    }
}

fn human_bytes(n: u64) -> String {
    if n >= 1 << 30 {
        format!("{:.1} GiB", n as f64 / (1 << 30) as f64)
    } else if n >= 1 << 20 {
        format!("{:.1} MiB", n as f64 / (1 << 20) as f64)
    } else if n >= 1 << 10 {
        format!("{:.1} KiB", n as f64 / (1 << 10) as f64)
    } else {
        format!("{n} B")
    }
}

/// Parse an RFC3339 timestamp ("2026-08-10T19:37:45.136Z") into epoch seconds.
/// Uses Howard Hinnant's `days_from_civil` (C++20 chrono) algorithm, which is
/// exact for all years 0000–9999.
fn parse_rfc3339(s: &str) -> Option<i64> {
    let s = s.trim_end_matches('Z').trim_end_matches('z');
    let (date, time) = s.split_once('T')?;
    let mut date_it = date.split('-');
    let year: i64 = date_it.next()?.parse().ok()?;
    let month: u32 = date_it.next()?.parse().ok()?;
    let day: u32 = date_it.next()?.parse().ok()?;

    let time = time.split('+').next()?;
    let mut time_it = time.split(':');
    let hour: u32 = time_it.next()?.parse().ok()?;
    let min: u32 = time_it.next()?.parse().ok()?;
    let sec_f = time_it.next()?;
    let sec: u64 = sec_f.split('.').next()?.parse().ok()?;

    let m = month as i64;
    let d = day as i64;
    let y = if m <= 2 { year - 1 } else { year };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let doy: i64 = (153 * (if m > 2 { m - 3 } else { m + 9 }) + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146097 + doe - 719468;
    Some(days * 86400 + hour as i64 * 3600 + min as i64 * 60 + sec as i64)
}
