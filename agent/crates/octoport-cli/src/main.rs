//! octoport — open local ports to the public internet on a random subdomain.
//!
//! Subcommands:
//!   login, whoami, logout, expose <port>, list, history, delete <id>,
//!   pause <id>, resume <id>
//!
//! `login` signs you in through the browser using the platform's
//! authentication page — the CLI never asks for an email or password directly.
//!
//! Every command renders neofetch-style: the OctoPort mark on the left, the
//! command's output in the column to its right.

mod loader;
mod neofetch;

use std::process::ExitCode;
use std::time::Duration;

use anyhow::{anyhow, Result};
use clap::{Parser, Subcommand};
use loader::Loader;
use neofetch::{ASCII_ART, Neofetch};
use octoport_core::{Client, Settings, client::AuthResponse, store};
use tracing_subscriber::EnvFilter;

/// Format an error for user-friendly display, providing helpful context
/// for common failure scenarios.
fn format_error(err: &anyhow::Error) -> String {
    use std::fmt::Write;
    let mut out = String::new();
    
    // Check for specific error types and provide helpful messages
    let err_str = err.to_string();
    
    // Check for connection/connection refused errors
    if err_str.contains("connection refused") 
        || err_str.contains("connection reset")
        || err_str.contains("connection timed out")
        || err_str.contains("connect error")
        || err_str.contains("tcp connect error") {
        writeln!(out, "❌ Cannot connect to OctoPort control plane").ok();
        writeln!(out, "").ok();
        writeln!(out, "The control plane at https://octoport-control-plane.itanishq.space appears to be unreachable.").ok();
        writeln!(out, "").ok();
        writeln!(out, "Possible causes:").ok();
        writeln!(out, "  • The control plane service is down or starting up").ok();
        writeln!(out, "  • Network connectivity issues (check your internet connection)").ok();
        writeln!(out, "  • The service may be temporarily unavailable").ok();
        writeln!(out, "").ok();
        writeln!(out, "Try again in a few moments. If the problem persists, check the status at:").ok();
        writeln!(out, "  https://octoport.itanishq.space/status").ok();
        return out;
    }
    
    // Check for 502 Bad Gateway (gateway/proxy issues)
    if err.to_string().contains("502") || err.to_string().contains("Bad Gateway") {
        writeln!(out, "❌ Control plane returned 502 Bad Gateway").ok();
        writeln!(out, "").ok();
        writeln!(out, "The control plane is receiving requests but the upstream service is unavailable.").ok();
        writeln!(out, "This usually means the backend services (database, cache) are not ready yet.").ok();
        writeln!(out, "").ok();
        writeln!(out, "Please wait a few minutes and try again.").ok();
        return out;
    }
    
    // Check for 503 Service Unavailable
    if err.to_string().contains("503") || err_str.contains("Service Unavailable") {
        writeln!(out, "❌ Service temporarily unavailable").ok();
        writeln!(out, "").ok();
        writeln!(out, "The control plane is temporarily unavailable, likely due to maintenance or high load.").ok();
        writeln!(out, "Please try again in a few minutes.").ok();
        return out;
    }
    
    // Check for 504 Gateway Timeout
    if err.to_string().contains("504") || err_str.contains("Gateway Timeout") {
        writeln!(out, "❌ Request timed out").ok();
        writeln!(out, "").ok();
        writeln!(out, "The control plane took too long to respond. This may indicate high load or a slow database.").ok();
        writeln!(out, "Please try again in a moment.").ok();
        return out;
    }
    
    // Check for DNS resolution errors
    if err_str.contains("dns") || err_str.contains("Name or service not known") {
        writeln!(out, "❌ Cannot resolve control plane hostname").ok();
        writeln!(out, "").ok();
        writeln!(out, "Cannot resolve octoport-control-plane.itanishq.space").ok();
        writeln!(out, "Check your DNS settings or try using a different DNS server (e.g., 1.1.1.1 or 8.8.8.8).").ok();
        return out;
    }
    
    // Check for TLS/SSL errors
    if err_str.contains("tls") || err_str.contains("ssl") || err_str.contains("certificate") {
        writeln!(out, "❌ TLS/SSL certificate error").ok();
        writeln!(out, "").ok();
        writeln!(out, "There was a problem establishing a secure connection to the control plane.").ok();
        writeln!(out, "This may be due to an expired certificate or a man-in-the-middle attack.").ok();
        writeln!(out, "Try again later or contact support if this persists.").ok();
        return out;
    }
    
    // Check for authentication errors
    if err_str.contains("401") || err_str.contains("Unauthorized") || err_str.contains("unauthorized") {
        writeln!(out, "❌ Authentication failed").ok();
        writeln!(out, "").ok();
        writeln!(out, "Your session has expired or is invalid. Please run `octoport login` to sign in again.").ok();
        return out;
    }
    
    // Check for 403 Forbidden
    if err_str.contains("403") || err_str.contains("Forbidden") {
        writeln!(out, "❌ Access forbidden").ok();
        writeln!(out, "").ok();
        writeln!(out, "You don't have permission to perform this action.").ok();
        writeln!(out, "Make sure you're signed in with the correct account.").ok();
        return out;
    }
    
    // Check for 404 Not Found
    if err_str.contains("404") || err_str.contains("Not Found") {
        writeln!(out, "❌ Resource not found").ok();
        writeln!(out, "").ok();
        writeln!(out, "The requested resource doesn't exist. It may have been deleted or the URL is incorrect.").ok();
        return out;
    }
    
    // Check for JSON decoding errors
    if err_str.contains("error decoding response body") || err_str.contains("expected value at line 1 column 1") {
        writeln!(out, "❌ Received unexpected response from control plane").ok();
        writeln!(out, "").ok();
        writeln!(out, "The control plane returned an unexpected response (likely an HTML error page instead of JSON).").ok();
        writeln!(out, "This usually means the service is down or returning an error page instead of JSON.").ok();
        writeln!(out, "").ok();
        writeln!(out, "Try again in a few moments. If the problem persists, the control plane may be down.").ok();
        return out;
    }
    
    // Check for timeout errors
    if err_str.contains("timed out") || err_str.contains("timeout") {
        writeln!(out, "❌ Request timed out").ok();
        writeln!(out, "").ok();
        writeln!(out, "The request took too long to complete. The control plane may be overloaded.").ok();
        writeln!(out, "Try again in a few moments.").ok();
        return out;
    }
    
    // Generic fallback - show the original error
    format!("❌ {err:#}")
}

#[derive(Parser)]
#[command(name = "octoport", version, about = "Open local ports to the public internet on random subdomains")]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    /// Sign in (or create an account) through the browser.
    Login,
    /// Show the signed-in account.
    Whoami,
    /// Forget the stored credentials.
    Logout,
    /// Expose a local port and stream traffic until interrupted.
    Expose {
        /// Local port (or host:port) to expose.
        port: String,
        /// Tunnel protocol: http (default) or tcp.
        #[arg(long, default_value = "http")]
        protocol: String,
    },
    /// List this account's live tunnels.
    List,
    /// Show this account's tunnel history (stored on the server).
    History {
        /// Number of most-recent events to show.
        #[arg(long, default_value_t = 25)]
        limit: u32,
    },
    /// Delete a tunnel by id.
    Delete { id: String },
    /// Pause a tunnel: stops routing traffic but keeps the subdomain reserved.
    Pause { id: String },
    /// Resume a paused tunnel so it serves traffic again.
    Resume { id: String },
}

#[tokio::main]
async fn main() -> ExitCode {
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()))
        .init();

    let cli = Cli::parse();
    match run(cli).await {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("{}", format_error(&e));
            ExitCode::FAILURE
        }
    }
}

async fn run(cli: Cli) -> Result<()> {
    // Service endpoints and protocol knobs are compiled into the binary; the
    // shipped CLI deliberately exposes no way to redirect them.
    let client = Client::new(Settings::default().api_url);
    let nf = Neofetch::new(ASCII_ART);

    match cli.command {
        Command::Login => {
            let auth = browser_login(&client, &nf).await?;
            save_session(&auth)?;
            println!("{}signed in as {} (plan: {})",
                nf.blank(),
                auth.email, auth.plan);
        }
        Command::Whoami => {
            let auth = require_auth()?;
            let client = client.with_token(&auth.token);

            // Fast path: answer from the local profile/tunnel cache so the
            // command feels instant even when the control plane is slow.
            let (me, tunnels) = match (store::cached_profile(), store::cached_tunnels()) {
                (Some(me), Some(tunnels)) => (me, tunnels),
                _ => {
                    let mut loader = Loader::start("fetching profile");
                    let (me, tunnels) = fetch_profile(&client).await?;
                    loader.done().await;
                    (me, tunnels)
                }
            };

            let active = tunnels.len();
            let max = me.max_tunnels.max(0) as usize;
            let limit_left = max.saturating_sub(active);

            let mut right = vec![
                format!("email:       {}", me.email),
                format!("plan:        {}", me.plan),
                format!("limit left:  {limit_left}"),
                format!("usage:       {active}/{max}"),
            ];

            let expiring = expiring_lines(&tunnels);
            if !expiring.is_empty() {
                right.push("expiring:".to_string());
                right.extend(expiring.into_iter().map(|l| format!("  {l}")));
            }
            println!("{}", nf.banner(&right));
        }
        Command::Logout => {
            store::clear_auth()?;
            println!("signed out");
        }
        Command::List => {
            let auth = require_auth()?;
            let client = client.with_token(&auth.token);
            let tunnels = match store::cached_tunnels() {
                Some(t) => t,
                None => {
                    let mut loader = Loader::start("listing tunnels");
                    let tunnels = client.list_tunnels().await?;
                    loader.done().await;
                    store::save_cached_tunnels(&tunnels);
                    tunnels
                }
            };
            if tunnels.is_empty() {
                println!("no tunnels");
            } else {
                let rows: Vec<Vec<String>> = tunnels
                    .iter()
                    .map(|t| {
                        let state = if !t.enabled {
                            "paused"
                        } else if t.bound {
                            "live"
                        } else {
                            "awaiting agent"
                        };
                        vec![
                            t.id.clone(),
                            t.url.clone(),
                            t.protocol.clone(),
                            t.local_addr.clone(),
                            state.to_string(),
                        ]
                    })
                    .collect();
                println!("{}", render_table(&["ID", "URL", "PROTO", "LOCAL", "STATE"], &rows));
            }
        }
        Command::Delete { id } => {
            let auth = require_auth()?;
            let client = client.with_token(&auth.token);
            let mut loader = Loader::start("deleting tunnel");
            let res = client.delete_tunnel(&id).await;
            loader.done().await;
            res?;
            store::invalidate_tunnels_cache();
            println!("deleted tunnel {id}");
        }
        Command::Pause { id } => {
            let auth = require_auth()?;
            let client = client.with_token(&auth.token);
            let mut loader = Loader::start("pausing tunnel");
            let res = client.set_tunnel_enabled(&id, false).await;
            loader.done().await;
            res?;
            store::invalidate_tunnels_cache();
            println!("paused tunnel {id} (subdomain kept reserved)");
        }
        Command::Resume { id } => {
            let auth = require_auth()?;
            let client = client.with_token(&auth.token);
            let mut loader = Loader::start("resuming tunnel");
            let res = client.set_tunnel_enabled(&id, true).await;
            loader.done().await;
            res?;
            store::invalidate_tunnels_cache();
            println!("resumed tunnel {id}");
        }
        Command::History { limit } => {
            let auth = require_auth()?;
            let client = client.with_token(&auth.token);
            let mut loader = Loader::start("fetching history");
            let events = client.list_events(limit as usize).await;
            loader.done().await;
            let events = events?;
            if events.is_empty() {
                println!("no tunnel history yet");
            } else {
                let rows: Vec<Vec<String>> = events
                    .iter()
                    .map(|e| {
                        let sub = e
                            .payload
                            .get("subdomain")
                            .and_then(|v| v.as_str())
                            .unwrap_or_default();
                        let proto = e
                            .payload
                            .get("protocol")
                            .and_then(|v| v.as_str())
                            .unwrap_or_default();
                        let kind = e.kind.replace("tunnel.", "");
                        vec![
                            e.created_at.clone(),
                            kind,
                            proto.to_string(),
                            sub.to_string(),
                        ]
                    })
                    .collect();
                println!(
                    "{}",
                    render_table(&["TIME", "KIND", "PROTO", "SUBDOMAIN"], &rows)
                );
            }
        }
        Command::Expose { port, protocol } => {
            if protocol != "http" && protocol != "tcp" {
                return Err(anyhow!("protocol must be 'http' or 'tcp'"));
            }
            let auth = require_auth()?;

            let mut settings = Settings::default();
            settings.normalize_addr(&port);
            settings.protocol = protocol.clone();

            // 1. allocate a random subdomain
            let client = client.with_token(&auth.token);
            let mut loader = Loader::start("allocating subdomain");
            let created = client.create_tunnel(&settings.local_addr, &settings.protocol, None, None).await;
            loader.done().await;
            let tunnel = created?;
            store::invalidate_tunnels_cache();
            println!("Public URL:  {}", tunnel.url);
            println!("Exposing {protocol}://{} (tunnel {})", settings.local_addr, tunnel.id);
            println!("Tunnel expires after 5 minutes of inactivity. Press Ctrl+C to stop.");

            // 2. mint a short-lived agent-scoped token, connect, and stream
            let mut loader = Loader::start("connecting agent");
            let agent_token = client.agent_token().await;
            loader.done().await;
            let agent_token = agent_token?;
            let agent = octoport_core::Agent::connect(&settings.ws_url, &agent_token, settings.max_frame_size, settings.max_streams)
                .await?;
            agent.run().await?;
        }
    }
    Ok(())
}

/// Describe the tunnels that are about to expire, either because their hard
/// lifetime is nearly up or because they've sat idle close to the control
/// plane's idle-dissolve window. Each returned line is already indented.
fn expiring_lines(tunnels: &[octoport_core::client::TunnelListItem]) -> Vec<String> {
    // Mirrors the control plane's defaults: links dissolve after 5 minutes of
    // inactivity and a tunnel hard-ends at its expiresAt cap.
    const IDLE_TIMEOUT: Duration = Duration::from_secs(5 * 60);
    const EXPIRING_WINDOW: Duration = Duration::from_secs(30 * 60);

    let now = std::time::SystemTime::now();

    let mut out = Vec::new();
    for t in tunnels {
        // Hard-lifetime: about to hit expiresAt.
        if let Some(expires) = parse_rfc3339(&t.expires_at) {
            if let Ok(remaining) = expires.duration_since(now) {
                if remaining <= EXPIRING_WINDOW {
                    out.push(format!("- {}  hard limit in {}", t.subdomain, human_duration(remaining)));
                    continue;
                }
            }
        }
        // Inactivity: last traffic is close to the idle-dissolve window.
        if t.enabled && t.bound && t.last_active_at > 0 {
            let last = std::time::UNIX_EPOCH
                .checked_add(std::time::Duration::from_secs(t.last_active_at as u64));
            if let Some(last) = last {
                if let Ok(idle) = now.duration_since(last) {
                    if idle >= IDLE_TIMEOUT.saturating_sub(Duration::from_secs(60)) {
                        out.push(format!("- {}  idle {}, will dissolve soon", t.subdomain, human_duration(idle)));
                    }
                }
            }
        }
    }
    out
}

/// Parse an RFC3339 timestamp (as the control plane emits for `expiresAt`)
/// into a Unix time. Handles `Z` and numeric `±HH:MM` offsets, with or without
/// fractional seconds.
fn parse_rfc3339(s: &str) -> Option<std::time::SystemTime> {
    let s = s.trim();
    let (date, rest) = s.split_once('T')?;
    let mut dparts = date.split('-');
    let year: i64 = dparts.next()?.parse().ok()?;
    let month: u32 = dparts.next()?.parse().ok()?;
    let day: u32 = dparts.next()?.parse().ok()?;
    if dparts.next().is_some() {
        return None;
    }

    // Time: HH:MM:SS[.fff][Z|±HH:MM|±HHMM]
    let (time, offset) = if let Some(t) = rest.strip_suffix('Z') {
        (t, 0)
    } else {
        // Within the time component the only +/- characters are the timezone
        // sign, so the last one marks the offset.
        let is_sign = |c: char| c == '+' || c == '-';
        let idx = rest.rfind(is_sign)?;
        let (t, tz) = rest.split_at(idx);
        let tz = &tz[1..];
        let (oh, om) = if let Some((h, m)) = tz.split_once(':') {
            let h: i32 = h.parse().ok()?;
            let m: i32 = m.parse().ok()?;
            (h, m)
        } else if tz.len() == 4 {
            (tz[0..2].parse().ok()?, tz[2..4].parse().ok()?)
        } else {
            return None;
        };
        let sign = if rest.as_bytes()[idx] == b'-' { -1 } else { 1 };
        (t, sign * (oh * 3600 + om * 60))
    };

    let mut tparts = time.split(':');
    let hour: u32 = tparts.next()?.parse().ok()?;
    let min: u32 = tparts.next()?.parse().ok()?;
    let sec: f64 = tparts.next()?.parse().ok()?;

    // Days from civil (proleptic Gregorian; Hinnant's algorithm).
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = month as i64 + if month > 2 { -3 } else { 9 };
    let doy = (153 * mp + 2) / 5 + day as i64 - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146097 + doe - 719468;

    let secs = days as f64 * 86400.0 + hour as f64 * 3600.0 + min as f64 * 60.0 + sec - offset as f64;
    std::time::UNIX_EPOCH
        .checked_add(std::time::Duration::from_secs_f64(secs))
}

/// Render a duration as "Xd Xh Xm Xs", trimming leading zero units.
fn human_duration(d: Duration) -> String {
    let mut s = d.as_secs();
    let mut out = Vec::new();
    for (unit, scale) in [("d", 86400u64), ("h", 3600), ("m", 60), ("s", 1)] {
        if s >= scale {
            out.push(format!("{}{}", s / scale, unit));
            s %= scale;
        }
    }
    if out.is_empty() {
        "0s".to_string()
    } else {
        out.join(" ")
    }
}

/// Render rows as a left-aligned table with auto-sized columns, Docker
/// `ps`-style: a header row followed by one row per tunnel/event.
fn render_table(headers: &[&str], rows: &[Vec<String>]) -> String {
    let cols = headers.len();
    let mut widths: Vec<usize> = headers.iter().map(|h| h.chars().count()).collect();
    for row in rows {
        for (i, cell) in row.iter().enumerate().take(cols) {
            widths[i] = widths[i].max(cell.chars().count());
        }
    }
    let mut out = String::new();
    let mut push = |cells: &[&str]| {
        for (i, cell) in cells.iter().enumerate() {
            if i > 0 {
                out.push_str("  ");
            }
            out.push_str(cell);
            if i + 1 < cols {
                out.push_str(&" ".repeat(widths[i].saturating_sub(cell.chars().count())));
            }
        }
        out.push('\n');
    };
    push(headers);
    for row in rows {
        let cells: Vec<&str> = row.iter().map(String::as_str).collect();
        push(&cells);
    }
    out
}

fn require_auth() -> Result<store::StoredAuth> {
    store::load_auth()?.ok_or_else(|| anyhow!("not signed in; run `octoport login` first"))
}

/// Fetch the signed-in profile and tunnel list, seeding the local cache so
/// subsequent `whoami` calls answer instantly.
async fn fetch_profile(client: &Client) -> Result<(octoport_core::client::Me, Vec<octoport_core::client::TunnelListItem>)> {
    let me = client.me().await?;
    let tunnels = client.list_tunnels().await?;
    store::save_cached_profile(&me);
    store::save_cached_tunnels(&tunnels);
    Ok((me, tunnels))
}

/// Persist a fresh session the same way register/login always have.
fn save_session(auth: &AuthResponse) -> Result<()> {
    store::save_auth(&store::StoredAuth {
        email: auth.email.clone(),
        token: auth.token.clone(),
        theme: None,
        auth_provider: Some("email".into()),
        avatar: auth.avatar.clone(),
        expires_at: Some(auth.expires_at.clone()),
        dark_mode: None,
    })
}

/// `octoport login` opens the platform's sign-in page in the
/// browser — the same email/password + GitHub flow the desktop app uses — and
/// wait for the terminal session to resolve. No credentials are ever handled
/// by the CLI itself.
async fn browser_login(client: &Client, nf: &Neofetch) -> Result<AuthResponse> {
    let start = client.cli_login_start().await?;
    let uri = client.cli_login_uri(&start.device);

    let right = vec![
        "Opening your browser to sign in…".to_string(),
        format!("If the browser doesn't open, visit: {uri}"),
    ];
    println!("{}", nf.banner(&right));
    open_browser(&uri);

    // The browser flow can sit open for a while; poll until it resolves or the
    // server-side session expires (~10 minutes).
    let mut attempts = 0u32;
    let mut loader = Loader::start("waiting for browser");
    loop {
        tokio::time::sleep(Duration::from_secs(2)).await;
        attempts += 1;
        match client.cli_login_poll(&start.device).await? {
            octoport_core::client::CliPoll::Pending => {
                if attempts > 300 {
                    loader.done().await;
                    return Err(anyhow!("sign-in timed out, please try again"));
                }
            }
            octoport_core::client::CliPoll::Done(auth) => {
                loader.done().await;
                return Ok(auth);
            }
        }
    }
}

/// Open the platform default browser at `url` without blocking the terminal.
#[cfg(target_os = "macos")]
fn open_browser(url: &str) {
    let _ = std::process::Command::new("open").arg(url).spawn();
}

#[cfg(target_os = "windows")]
fn open_browser(url: &str) {
    let _ = std::process::Command::new("cmd").args(["/start", "", url]).spawn();
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
fn open_browser(url: &str) {
    let _ = std::process::Command::new("xdg-open").arg(url).spawn();
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::UNIX_EPOCH;

    fn unix(s: &str) -> u64 {
        parse_rfc3339(s)
            .unwrap()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs()
    }

    #[test]
    fn parse_utc_zulu() {
        assert_eq!(unix("1970-01-01T00:00:00Z"), 0);
        assert_eq!(unix("2024-02-29T12:00:00Z"), 1709208000);
    }

    #[test]
    fn parse_numeric_offset() {
        // 2024-02-29T12:00:00+02:00 == 10:00 UTC
        assert_eq!(unix("2024-02-29T12:00:00+02:00"), 1709200800);
        // ...-05:00 == 17:00 UTC
        assert_eq!(unix("2024-02-29T12:00:00-05:00"), 1709226000);
    }

    #[test]
    fn parse_fractional_seconds() {
        assert_eq!(unix("2024-02-29T12:00:00.5Z"), 1709208000);
    }

    #[test]
    fn parse_compact_offset() {
        assert_eq!(unix("2024-02-29T12:00:00+0200"), 1709200800);
    }

    #[test]
    fn parse_rejects_garbage() {
        assert!(parse_rfc3339("").is_none());
        assert!(parse_rfc3339("yesterday").is_none());
        assert!(parse_rfc3339("2024-02-29").is_none());
    }

    #[test]
    fn human_durations() {
        assert_eq!(human_duration(Duration::from_secs(0)), "0s");
        assert_eq!(human_duration(Duration::from_secs(59)), "59s");
        assert_eq!(human_duration(Duration::from_secs(60)), "1m");
        assert_eq!(human_duration(Duration::from_secs(3725)), "1h 2m 5s");
        assert_eq!(human_duration(Duration::from_secs(90061)), "1d 1h 1m 1s");
    }
}