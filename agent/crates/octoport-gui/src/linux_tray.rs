//! Linux system tray via the freedesktop StatusNotifierItem spec (ksni).
//!
//! This replaces the dioxus/muda tray path on Linux, where libappindicator is
//! unavailable (the app was starting up with no tray icon at all). The menu is
//! rebuilt whenever tunnels or auth change by pushing a fresh [`TrayState`]
//! through [`ksni::Handle::update`]; activation commands flow back to the main
//! thread over the command channel.

use image::GenericImageView;
use ksni::menu::{MenuItem, StandardItem, SubMenu};
use ksni::{Category, Icon, Status};

/// Menu command ids, mirrored from the app's tray constants so the app's
/// `handle_tray_event` can route them unchanged.
pub const TRAY_OPEN: &str = "open";
pub const TRAY_ABOUT: &str = "about";
pub const TRAY_SIGNIN: &str = "signin";
pub const TRAY_SIGNOUT: &str = "signout";
pub const TRAY_QUIT: &str = "quit";
pub const TRAY_TUNNEL_PREFIX: &str = "tunnel:";
pub const TRAY_PAUSE: &str = ":pause";
pub const TRAY_RESUME: &str = ":resume";

/// The subset of app state the tray menu renders. Plain owned data so it can
/// travel from the main thread to the SNI service thread.
#[derive(Clone, Default)]
pub struct TrayState {
    pub email: Option<String>,
    pub tunnels: Vec<TunnelRow>,
}

#[derive(Clone)]
pub struct TunnelRow {
    pub id: String,
    pub subdomain: String,
    pub enabled: bool,
    pub bound: bool,
}

/// The StatusNotifierItem served by ksni.
///
/// `menu()` runs on the SNI service thread; it only reads the current
/// [`TrayState`] and builds `StandardItem`s whose `activate` closures forward
/// the command id to the main thread over `tx`.
pub struct LinuxTray {
    tx: std::sync::mpsc::Sender<String>,
    state: TrayState,
}

impl LinuxTray {
    pub fn new(tx: std::sync::mpsc::Sender<String>, state: TrayState) -> Self {
        Self { tx, state }
    }

    /// Replace the menu-driving state. Called via [`ksni::Handle::update`]
    /// from the main thread whenever tunnels or auth change; the service then
    /// re-emits the menu to the host.
    pub fn update_state(&mut self, state: TrayState) {
        self.state = state;
    }
}

impl ksni::Tray for LinuxTray {
    fn id(&self) -> String {
        "octoport".into()
    }

    fn title(&self) -> String {
        "OctoPort".into()
    }

    fn category(&self) -> Category {
        Category::ApplicationStatus
    }

    fn status(&self) -> Status {
        Status::Active
    }

    fn icon_pixmap(&self) -> Vec<Icon> {
        match image::load_from_memory_with_format(
            include_bytes!("../assets/octoport-tray-icon.png"),
            image::ImageFormat::Png,
        ) {
            Ok(img) => {
                let (w, h) = img.dimensions();
                let mut data = img.into_rgba8().into_vec();
                for px in data.chunks_exact_mut(4) {
                    px.rotate_right(1); // rgba -> argb, network byte order
                }
                vec![Icon {
                    width: w as i32,
                    height: h as i32,
                    data,
                }]
            }
            Err(_) => vec![],
        }
    }

    /// Left-click opens the app window.
    fn activate(&mut self, _x: i32, _y: i32) {
        let _ = self.tx.send(TRAY_OPEN.to_string());
    }

    fn menu(&self) -> Vec<MenuItem<Self>> {
        let mut items: Vec<MenuItem<Self>> = vec![
            standard(TRAY_OPEN, "Open OctoPort", true),
            standard(TRAY_ABOUT, "About OctoPort", true),
            MenuItem::Separator,
        ];

        match &self.state.email {
            Some(email) => {
                items.push(
                    StandardItem {
                        label: format!("Signed in as {email}"),
                        enabled: false,
                        ..Default::default()
                    }
                    .into(),
                );
                items.push(standard(TRAY_SIGNOUT, "Sign out", true));
            }
            None => {
                items.push(standard(TRAY_SIGNIN, "Sign in\u{2026}", true));
            }
        }
        items.push(MenuItem::Separator);

        let tunnel_items: Vec<MenuItem<Self>> = if self.state.tunnels.is_empty() {
            vec![StandardItem {
                label: "No running tunnels".into(),
                enabled: false,
                ..Default::default()
            }
            .into()]
        } else {
            self.state
                .tunnels
                .iter()
                .map(|t| {
                    let (label, id) = if t.enabled {
                        (
                            format!(
                                "{}  [{}]",
                                t.subdomain,
                                if t.bound { "live" } else { "awaiting agent" }
                            ),
                            format!(
                                "{}{}{}",
                                TRAY_TUNNEL_PREFIX, t.id, TRAY_PAUSE
                            ),
                        )
                    } else {
                        (
                            format!("{}  [paused]", t.subdomain),
                            format!(
                                "{}{}{}",
                                TRAY_TUNNEL_PREFIX, t.id, TRAY_RESUME
                            ),
                        )
                    };
                    standard(&id, &label, true)
                })
                .collect()
        };
        items.push(
            SubMenu {
                label: "Running Tunnels".into(),
                submenu: tunnel_items,
                ..Default::default()
            }
            .into(),
        );

        items.push(MenuItem::Separator);
        items.push(standard(TRAY_QUIT, "Quit OctoPort", true));
        items
    }
}

/// Build a clickable menu row whose activation sends `id` back to the app.
fn standard(id: &str, label: &str, enabled: bool) -> MenuItem<LinuxTray> {
    let id = id.to_string();
    StandardItem {
        label: label.to_string(),
        enabled,
        activate: Box::new(move |this: &mut LinuxTray| {
            let _ = this.tx.send(id.clone());
        }),
        ..Default::default()
    }
    .into()
}
