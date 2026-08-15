//! Local credential store: keeps the bearer token and account email on disk so
//! the CLI / GUI stay signed in between runs. Also persists a small, short-lived
//! cache of profile + tunnel data so commands can answer instantly without
//! round-tripping the control plane every time.

use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};

use crate::client::{Me, TunnelListItem};

const DEFAULT_FILE: &str = "config.json";
const CACHE_FILE: &str = "cache.json";
/// Profile cache lifetime. Account profile (email, plan, limits) barely
/// changes, so a generous TTL lets `whoami` answer instantly for a long time.
const PROFILE_TTL_SECS: u64 = 6 * 3600;
/// Tunnel cache lifetime. Tunnel state is volatile (paused/live/awaiting),
/// so it stays fresh for only a short window.
const TUNNEL_TTL_SECS: u64 = 60;

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

// ---------------------------------------------------------------------------
// Local cache: profile + tunnels so the CLI answers instantly.
// ---------------------------------------------------------------------------

/// On-disk shape of the local cache. Both fields are optional so a partial
/// write never corrupts the whole file; missing/old entries simply mean "go
/// fetch from the control plane".
#[derive(Debug, Default, Clone, Serialize, Deserialize)]
pub struct LocalCache {
    #[serde(default)]
    pub profile: Option<CachedEntry<Me>>,
    #[serde(default)]
    pub tunnels: Option<CachedEntry<Vec<TunnelListItem>>>,
}

/// A single cached value plus the unix-seconds timestamp it was saved at.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CachedEntry<T> {
    pub saved_at: u64,
    pub value: T,
}

impl<T> CachedEntry<T> {
    /// True while the entry is younger than `ttl_secs` (computed against
    /// `now_secs`), i.e. still safe to serve without a network round-trip.
    pub fn fresh(&self, ttl_secs: u64, now: u64) -> bool {
        now.saturating_sub(self.saved_at) < ttl_secs
    }
}

/// Location of the cache file, e.g. `~/.config/octoport/cache.json`.
pub fn cache_path() -> Result<PathBuf> {
    Ok(config_dir()?.join(CACHE_FILE))
}

/// Load the cached profile, if it is still within its TTL.
pub fn cached_profile() -> Option<Me> {
    let cache = read_cache().ok()?;
    let entry = cache.profile.as_ref()?;
    if !entry.fresh(PROFILE_TTL_SECS, now_secs()) {
        return None;
    }
    Some(entry.value.clone())
}

/// Load the cached tunnel list, if it is still within its TTL.
pub fn cached_tunnels() -> Option<Vec<TunnelListItem>> {
    let cache = read_cache().ok()?;
    let entry = cache.tunnels.as_ref()?;
    if !entry.fresh(TUNNEL_TTL_SECS, now_secs()) {
        return None;
    }
    Some(entry.value.clone())
}

/// Persist a freshly-fetched profile into the local cache.
pub fn save_cached_profile(me: &Me) {
    let mut cache = read_cache().unwrap_or_default();
    cache.profile = Some(CachedEntry {
        saved_at: now_secs(),
        value: me.clone(),
    });
    let _ = write_cache(&cache);
}

/// Persist a freshly-fetched tunnel list into the local cache.
pub fn save_cached_tunnels(tunnels: &[TunnelListItem]) {
    let mut cache = read_cache().unwrap_or_default();
    cache.tunnels = Some(CachedEntry {
        saved_at: now_secs(),
        value: tunnels.to_vec(),
    });
    let _ = write_cache(&cache);
}

/// Drop the cached tunnel list (e.g. after create/delete/pause/resume) so the
/// next `list` reflects reality immediately rather than a stale snapshot.
pub fn invalidate_tunnels_cache() {
    let mut cache = read_cache().unwrap_or_default();
    cache.tunnels = None;
    let _ = write_cache(&cache);
}

fn read_cache() -> Result<LocalCache> {
    let path = cache_path()?;
    if !path.exists() {
        return Ok(LocalCache::default());
    }
    let raw = std::fs::read_to_string(&path).context("reading cache file")?;
    Ok(serde_json::from_str(&raw).context("parsing cache file")?)
}

fn write_cache(cache: &LocalCache) -> Result<()> {
    let path = cache_path()?;
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).context("creating config dir")?;
    }
    let raw = serde_json::to_string_pretty(cache)?;
    std::fs::write(&path, raw).context("writing cache file")?;
    Ok(())
}

fn now_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cache_entry_fresh_within_ttl() {
        let entry = CachedEntry { saved_at: 1000, value: 7u32 };
        // Just under the TTL boundary is still fresh.
        assert!(entry.fresh(60, 1000 + 59));
        // At the boundary the cache is considered stale.
        assert!(!entry.fresh(60, 1000 + 60));
        // Ancient entries are always stale.
        assert!(!entry.fresh(3600, 1000 + 7200));
    }

    #[test]
    fn cache_entry_round_trips_json() {
        let entry = CachedEntry {
            saved_at: 42,
            value: vec!["a".to_string(), "b".to_string()],
        };
        let raw = serde_json::to_string(&entry).unwrap();
        let back: CachedEntry<Vec<String>> = serde_json::from_str(&raw).unwrap();
        assert_eq!(back.saved_at, 42);
        assert_eq!(back.value, vec!["a".to_string(), "b".to_string()]);
    }

    #[test]
    fn local_cache_defaults_empty() {
        let cache = LocalCache::default();
        assert!(cache.profile.is_none());
        assert!(cache.tunnels.is_none());
    }
}
