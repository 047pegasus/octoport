//! A single-line terminal loader in the spirit of Claude Code's spinner, ported
//! from the `octoport_loader.py` mock: a glyph that morphs shape while its color
//! breathes through a cyan -> violet gradient, next to a rotating status phrase
//! and an elapsed timer, all redrawn in place on one terminal line.
//!
//! The loader renders on stderr so stdout stays clean for machine-readable
//! output. Cursor is hidden while it runs and restored on exit, and the line is
//! cleared when the work finishes.

use std::io::{IsTerminal, Write};
use std::time::Duration;

use tokio::sync::oneshot;

/// The glyph: a small "spark" that morphs through increasingly busy shapes, the
/// same trick Claude Code's own spinner uses to read as "alive".
const GLYPHS: [&str; 8] = ["·", "✢", "✳", "✻", "✽", "✻", "✳", "✢"];

/// Rotating status phrases, themed to the app (tunnels/ports).
const PHRASES: [&str; 6] = [
    "waking the octopus",
    "reaching for a port",
    "punching through NAT",
    "negotiating handshake",
    "opening tunnel",
    "syncing streams",
];

/// Color: a cyan -> violet gradient that breathes back and forth rather than
/// hard-cutting, using a sine so the loop has no visible seam.
const COLOR_A: (u8, u8, u8) = (56, 189, 248); // cyan
const COLOR_B: (u8, u8, u8) = (168, 85, 247); // violet

/// Whether the terminal supports ANSI color output.
fn use_color() -> bool {
    if std::env::var_os("NO_COLOR").is_some() {
        return false;
    }
    std::io::stderr().is_terminal()
}

/// Render one loader frame as a single-line string (no trailing newline).
fn render_frame(frame_index: usize, elapsed: f64, phrase: &str, color: bool) -> String {
    let glyph = GLYPHS[frame_index % GLYPHS.len()];
    // If the caller gave a specific phrase, use it; otherwise rotate through
    // the built-in themed phrases.
    let label = if phrase.is_empty() {
        PHRASES[(frame_index / GLYPHS.len()) % PHRASES.len()]
    } else {
        phrase
    };
    // Color cycles independently and a bit faster than the phrase rotation, so
    // the two motions don't feel locked together.
    let t = (frame_index % 60) as f64 / 60.0;
    if color {
        let (r, g, b) = gradient_rgb(t);
        format!("\x1b[1m\x1b[38;2;{r};{g};{b}m{glyph}\x1b[0m {label}… ({elapsed:.0}s)")
    } else {
        format!("{glyph} {label}… ({elapsed:.0}s)")
    }
}

/// `t in [0, 1) -> (r, g, b)`, breathing between cyan and violet.
fn gradient_rgb(t: f64) -> (u8, u8, u8) {
    let phase = ((t * 2.0 * std::f64::consts::PI).sin() + 1.0) / 2.0; // 0 -> 1 -> 0, no seam
    let lerp = |a: u8, b: u8| a as f64 + (b as f64 - a as f64) * phase;
    (
        lerp(COLOR_A.0, COLOR_B.0).round() as u8,
        lerp(COLOR_A.1, COLOR_B.1).round() as u8,
        lerp(COLOR_A.2, COLOR_B.2).round() as u8,
    )
}

/// A running loader. Drop it (or call [`Loader::done`]) to stop the animation
/// and clear the line. The render loop runs on a background task; the caller's
/// async work proceeds on the current task concurrently.
pub struct Loader {
    stop: Option<oneshot::Sender<()>>,
    handle: Option<tokio::task::JoinHandle<()>>,
}

impl Loader {
    /// Start a loader with the given status phrase (empty string selects the
    /// rotating built-in phrases). Does nothing visible on non-terminals, but
    /// still reserves the drop-handle so `done()` is always safe to call.
    pub fn start(phrase: impl Into<String>) -> Self {
        let phrase = phrase.into();
        let color = use_color();
        let (tx, mut rx) = oneshot::channel::<()>();
        let handle = tokio::spawn(async move {
            let mut out = std::io::stderr();
            // Non-terminal: nothing to animate; just wait for the stop signal.
            if !std::io::stderr().is_terminal() {
                let _ = rx.await;
                return;
            }
            let _ = out.write_all(b"\x1b[?25l"); // hide cursor
            let mut frame = 0usize;
            let start = std::time::Instant::now();
            loop {
                let elapsed = start.elapsed().as_secs_f64();
                let line = render_frame(frame, elapsed, &phrase, color);
                let _ = out.write_all(b"\r\x1b[2K");
                let _ = out.write_all(line.as_bytes());
                let _ = out.flush();
                frame += 1;
                // Tick at ~11fps. `timeout` returns Err when the 90ms elapses
                // first (keep spinning) or Ok when the stop signal wins.
                if tokio::time::timeout(Duration::from_millis(90), &mut rx)
                    .await
                    .is_ok()
                {
                    break;
                }
            }
            let _ = out.write_all(b"\r\x1b[2K\x1b[?25h"); // clear line, restore cursor
            let _ = out.flush();
        });
        Loader {
            stop: Some(tx),
            handle: Some(handle),
        }
    }

    /// Stop the animation, clear the line and restore the cursor. Safe to call
    /// multiple times; a second call is a no-op.
    pub async fn done(&mut self) {
        if let Some(tx) = self.stop.take() {
            let _ = tx.send(());
        }
        if let Some(handle) = self.handle.take() {
            let _ = handle.await;
        }
    }
}

impl Drop for Loader {
    fn drop(&mut self) {
        // Best-effort stop on drop: send the signal so the render task exits.
        // The handle itself is detached (can't await in Drop); the task cleans
        // up its own cursor/line state when it observes the signal.
        if let Some(tx) = self.stop.take() {
            let _ = tx.send(());
        }
    }
}
