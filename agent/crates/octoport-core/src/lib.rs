//! octoport-core: the shared library behind the OctoPort agent CLI and desktop
//! app. It talks to the control plane (REST + WebSocket) and forwards traffic
//! between the public proxy and local TCP services.

pub mod agent;
pub mod client;
pub mod protocol;
pub mod settings;
pub mod store;

pub use agent::Agent;
pub use client::Client;
pub use settings::Settings;
