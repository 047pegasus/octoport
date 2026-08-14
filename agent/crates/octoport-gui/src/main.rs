//! OctoPort desktop app — a native, cross-platform GUI wrapping octoport-core.
//!
//! Built with Dioxus 0.7 (desktop renderer). The UI is HTML/CSS in a system
//! WebView; all state and networking run natively in Rust. The window is
//! frameless and draws its own titlebar so it matches the app's dark chrome.
//!
//! Build and run with:
//!   cargo build -p octoport-gui
//!   ./target/debug/octoport-app

#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use dioxus::desktop::{Config, LogicalSize, WindowBuilder, WindowCloseBehaviour, icon_from_memory};
use dioxus::prelude::*;

mod app;
#[cfg(target_os = "linux")]
mod linux_tray;
mod logo;
mod theme;

fn main() {
    // Initialize logger for debug output
    env_logger::Builder::from_env(env_logger::Env::default().default_filter_or("info"))
        .format_timestamp_millis()
        .init();

    // The OctoPort favicon doubles as the taskbar icon (and the tray icon).
    let app_icon = icon_from_memory::<dioxus::desktop::tao::window::Icon>(
        include_bytes!("../assets/octoport-app-icon.png"),
    )
    .expect("embedded app icon is valid");

    let cfg = Config::new()
        .with_window(
            WindowBuilder::new()
                .with_title("OctoPort")
                .with_decorations(false)
                .with_inner_size(LogicalSize::new(1120.0, 720.0))
                .with_min_inner_size(LogicalSize::new(880.0, 560.0)),
        )
        .with_icon(app_icon)
        // Closing the window hides it to the system tray instead of quitting:
        // the agent keeps tunneling until the user exits from the tray menu.
        .with_close_behaviour(WindowCloseBehaviour::WindowHides)
        .with_background_color((10, 10, 10, 255));

    LaunchBuilder::new().with_cfg(cfg).launch(app::App);
}
