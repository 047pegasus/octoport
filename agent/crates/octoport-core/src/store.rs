//! Local credential store: keeps the bearer token and account email on disk so
//! the CLI / GUI stay signed in between runs.

use std::path::PathBuf;

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};

const DEFAULT_FILE: &str = "config.json";

/// `Debug` is implemented by hand: this struct is the on-disk credential and
/// must never be printed with its token intact.
#[derive(Clone, Serialize, Deserialize)]
pub struct StoredAuth {
    pub email: String,
    pub token: String,
    /// UI appearance preference: "system" | "dark" | "light" (None = system).
    #[serde(default)]
    pub theme: Option<String>,
    /// How the account signed in: "github" | "email" (None = email).
    #[serde(default)]
    pub auth_provider: Option<String>,
    /// GitHub avatar URL for OAuth accounts; None keeps the initial badge.
    #[serde(default)]
    pub avatar: Option<String>,
    /// When the token was issued / next rotation point (informational).
    #[serde(default)]
    pub expires_at: Option<String>,
    /// Legacy field kept for configs written by older clients.
    #[serde(default)]
    pub dark_mode: Option<bool>,
}

impl std::fmt::Debug for StoredAuth {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("StoredAuth")
            .field("email", &self.email)
            .field("token", &"<redacted>")
            .field("theme", &self.theme)
            .field("auth_provider", &self.auth_provider)
            .field("avatar", &self.avatar)
            .field("expires_at", &self.expires_at)
            .field("dark_mode", &self.dark_mode)
            .finish()
    }
}

/// OctoPort's per-user config directory, e.g. `~/.config/octoport`.
pub fn config_dir() -> Result<PathBuf> {
    let dir = dirs::config_dir()
        .or_else(dirs::home_dir)
        .context("no config or home directory")?;
    Ok(dir.join("octoport"))
}

/// Location of the credentials file, e.g. `~/.config/octoport/config.json`.
pub fn config_path() -> Result<PathBuf> {
    Ok(config_dir()?.join(DEFAULT_FILE))
}

/// Load previously stored credentials.
pub fn load_auth() -> Result<Option<StoredAuth>> {
    let path = config_path()?;
    if !path.exists() {
        return Ok(None);
    }
    let raw = std::fs::read_to_string(&path).context("reading config file")?;
    let auth = serde_json::from_str(&raw).context("parsing config file")?;
    Ok(Some(auth))
}

/// Persist credentials to disk (0600 perms).
pub fn save_auth(auth: &StoredAuth) -> Result<()> {
    let path = config_path()?;
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).context("creating config dir")?;
    }
    let raw = serde_json::to_string_pretty(auth)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::write(&path, raw)?;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))?;
    }
    #[cfg(not(unix))]
    std::fs::write(&path, raw)?;
    Ok(())
}

/// Remove stored credentials (logout).
pub fn clear_auth() -> Result<()> {
    let path = config_path()?;
    if path.exists() {
        std::fs::remove_file(&path).context("removing config file")?;
    }
    Ok(())
}
