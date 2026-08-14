//! Runtime settings for the client.
//!
//! Resolution order (lowest to highest):
//!   1. compiled-in hosted defaults (so a fresh install "just works")
//!   2. `~/.config/octoport/settings.json` (dev / deployment overrides)
//!   3. `OCTOPORT_*` environment variables
//!   4. `octoport` CLI flags
//!
//! During local development a `settings.json` pointing at
//! `http://localhost:8080` / `ws://localhost:8081/agent/connect` is enough to
//! redirect every client without touching the shipped defaults. The config
//! file is optional; the installer never writes it.

use std::env;
use std::path::PathBuf;

use anyhow::Result;
use serde::Deserialize;

/// Settings that shape how the agent talks to a control plane.
#[derive(Debug, Clone)]
pub struct Settings {
    /// Base URL of the OctoPort API.
    pub api_url: String,
    /// WebSocket URL the client connects out to.
    pub ws_url: String,
    /// Domain appended to random labels to build public URLs.
    pub base_domain: String,
    /// Local address the agent dials, e.g. `127.0.0.1:3000`.
    pub local_addr: String,
    /// Tunnel protocol: `http` or `tcp`.
    pub protocol: String,
    /// Max frame payload accepted from the control plane.
    pub max_frame_size: usize,
    /// Max concurrent streams an agent will serve.
    pub max_streams: usize,
}

/// On-disk shape of the optional settings file (`settings.json`). Every field
/// is optional so a file may override just one endpoint.
#[derive(Debug, Default, Clone, Deserialize)]
pub struct SettingsFile {
    #[serde(default)]
    pub api_url: Option<String>,
    #[serde(default)]
    pub ws_url: Option<String>,
    #[serde(default)]
    pub base_domain: Option<String>,
}

/// Location of the optional settings file, e.g. `~/.config/octoport/settings.json`.
pub fn settings_path() -> Result<PathBuf> {
    Ok(crate::store::config_dir()?.join("settings.json"))
}

/// Hosted service endpoints, compiled in as the defaults.
///
/// In debug builds (cargo run, cargo build) these point to the local
/// control plane so the CLI/GUI works out of the box during development.
/// In release builds (cargo build --release) they point to the production
/// hosted service.
#[cfg(debug_assertions)]
pub const DEFAULT_API_URL: &str = "http://localhost:8080";
#[cfg(debug_assertions)]
pub const DEFAULT_WS_URL: &str = "ws://localhost:8081/agent/connect";
#[cfg(debug_assertions)]
pub const DEFAULT_BASE_DOMAIN: &str = "localhost";

#[cfg(not(debug_assertions))]
pub const DEFAULT_API_URL: &str = "https://octoport-control-plane.itanishq.space";
#[cfg(not(debug_assertions))]
pub const DEFAULT_WS_URL: &str = "wss://octoport-control-plane.itanishq.space/agent/connect";
#[cfg(not(debug_assertions))]
pub const DEFAULT_BASE_DOMAIN: &str = "itanishq.space";

impl Default for Settings {
    fn default() -> Self {
        Settings {
            api_url: DEFAULT_API_URL.into(),
            ws_url: DEFAULT_WS_URL.into(),
            base_domain: DEFAULT_BASE_DOMAIN.into(),
            local_addr: "127.0.0.1:3000".into(),
            protocol: "http".into(),
            max_frame_size: 1 << 20,
            max_streams: 64,
        }
    }
}

impl Settings {
    /// Build settings from the compiled-in defaults plus the optional
    /// `~/.config/octoport/settings.json` file. A missing or unparseable file
    /// is ignored — the client still boots with the hosted defaults.
    pub fn load() -> Result<Self> {
        let mut s = Settings::default();
        let path = settings_path()?;
        if path.exists() {
            match std::fs::read_to_string(&path) {
                Ok(raw) => match serde_json::from_str::<SettingsFile>(&raw) {
                    Ok(file) => {
                        s.apply_file(file);
                        tracing::debug!(path = %path.display(), "loaded octoport settings file");
                    }
                    Err(e) => {
                        tracing::warn!(path = %path.display(), error = %e, "ignoring invalid settings file");
                    }
                },
                Err(e) => {
                    tracing::warn!(path = %path.display(), error = %e, "could not read settings file");
                }
            }
        }
        s.apply_env();
        Ok(s)
    }

    /// Build settings from the hosted defaults only, letting `OCTOPORT_*`
    /// environment variables override them. Used for local development; unset
    /// in normal use.
    pub fn from_env() -> Self {
        Settings::load().unwrap_or_default()
    }

    fn apply_file(&mut self, file: SettingsFile) {
        if let Some(v) = file.api_url {
            self.api_url = v;
        }
        if let Some(v) = file.ws_url {
            self.ws_url = v;
        }
        if let Some(v) = file.base_domain {
            self.base_domain = v;
        }
    }

    fn apply_env(&mut self) {
        if let Ok(v) = env::var("OCTOPORT_API_URL") {
            self.api_url = v;
        }
        if let Ok(v) = env::var("OCTOPORT_WS_URL") {
            self.ws_url = v;
        }
        if let Ok(v) = env::var("OCTOPORT_BASE_DOMAIN") {
            self.base_domain = v;
        }
        if let Ok(v) = env::var("OCTOPORT_MAX_FRAME_SIZE") {
            if let Ok(n) = v.parse() {
                self.max_frame_size = n;
            }
        }
        if let Ok(v) = env::var("OCTOPORT_MAX_STREAMS") {
            if let Ok(n) = v.parse() {
                self.max_streams = n;
            }
        }
    }

    /// Derive the public URL a tunnel will be served at. Tunnels on the hosted
    /// service are always HTTPS; plain HTTP only appears when an override
    /// points the client at a local, non-TLS control plane.
    pub fn public_url(&self, subdomain: &str) -> String {
        let scheme = if self.api_url.starts_with("https") {
            "https"
        } else {
            "http"
        };
        format!("{scheme}://{subdomain}.{}", self.base_domain)
    }

    /// Normalise an address into `host:port`.
    pub fn normalize_addr(&mut self, addr: &str) {
        let addr = addr.trim();
        if addr.contains(':') {
            self.local_addr = addr.to_string();
        } else {
            self.local_addr = format!("localhost:{addr}");
        }
    }
}
