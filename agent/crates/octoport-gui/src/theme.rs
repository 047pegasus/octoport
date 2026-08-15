//! The OctoPort design system: a single stylesheet driven by CSS custom
//! properties so the whole app restyles instantly when `data-theme` flips.
//!
//! `themed_css()` returns a full `<style>` body. The root element carries a
//! `data-theme="dark" | "light"` attribute; every color used by components is
//! a `var(--x)` referencing these tokens. Surfaces are neutral grays; the only
//! brand color is periwinkle `#CCCCFF` (the website accent) via `--live`.

pub fn themed_css() -> &'static str {
    r#"
:root {
    --font: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Inter, "Helvetica Neue", sans-serif;
    --mono: ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas, monospace;

    /* dark theme (default) */
    --bg: #0a0a0a;
    --bg-elev: #111111;
    --panel: #181818;
    --panel-2: #1f1f1f;
    --card: #1a1a1a;
    --card-hover: #222222;
    --border: #2a2a2a;
    --border-strong: #3a3a3a;
    --text: #e8e8e8;
    --text-2: #a0a0a0;
    --muted: #707070;
    --accent: #ffffff;
    --accent-hover: #e0e0e0;
    --accent-soft: rgba(255, 255, 255, 0.1);
    --live: #CCCCFF;   /* periwinkle brand accent (matches website theme) */
    --danger: #f87171;
    --danger-soft: rgba(248, 113, 113, 0.16);
    --info: #60a5fa;
    --info-soft: rgba(96, 165, 250, 0.14);
    --shadow: 0 10px 30px rgba(0, 0, 0, 0.5);
    --input-bg: #0f0f0f;

    /* Chart palette: the Recharts engine reads these on
       <html> to color pie/radar/radial/area series. */
    --chart-1: #CCCCFF;
    --chart-2: #6EE7B7;
    --chart-3: #FBBF24;
    --chart-4: #F472B6;
    --chart-5: #60A5FA;
    --color-chart-primary: #CCCCFF;
    --color-chart-muted: #6EE7B7;
    --color-chart-grid: #2a2a2a;
    --color-chart-secondary: 240 100% 90%;
    --color-chart-tertiary: 158 72% 67%;
    --color-foreground: #e8e8e8;
    --color-muted-foreground: #a0a0a0;
    /* Native widgets (select dropdown, scrollbars) follow the theme. */
    color-scheme: dark;
}

[data-theme="light"] {
    --bg: #fafafa;
    --bg-elev: #ffffff;
    --panel: #ffffff;
    --panel-2: #f5f5f5;
    --card: #ffffff;
    --card-hover: #f5f5f5;
    --border: #e5e5e5;
    --border-strong: #d4d4d4;
    --text: #171717;
    --text-2: #525252;
    --muted: #737373;
    --accent: #000000;
    --accent-hover: #1a1a1a;
    --accent-soft: rgba(0, 0, 0, 0.06);
    --live: #6666d6;   /* periwinkle accent-ink for the light theme (readable on white) */
    --danger: #dc2626;
    --danger-soft: rgba(220, 38, 38, 0.12);
    --info: #2563eb;
    --info-soft: rgba(37, 99, 235, 0.12);
    --shadow: 0 10px 30px rgba(0, 0, 0, 0.08);
    --input-bg: #ffffff;

    --chart-1: #6666d6;
    --chart-2: #059669;
    --chart-3: #d97706;
    --chart-4: #db2777;
    --chart-5: #2563eb;
    --color-chart-primary: #6666d6;
    --color-chart-muted: #059669;
    --color-chart-grid: #e5e5e5;
    --color-chart-secondary: 240 47% 62%;
    --color-chart-tertiary: 158 64% 40%;
    --color-foreground: #171717;
    --color-muted-foreground: #525252;
    color-scheme: light;
}

* { box-sizing: border-box; }
html, body, #main { height: 100%; margin: 0; }
body {
    margin: 0;
    font-family: var(--font);
    background: var(--bg);
    color: var(--text);
    -webkit-font-smoothing: antialiased;
    overflow: hidden;
}

.app {
    height: 100vh;
    display: flex;
    flex-direction: column;
    background: var(--bg);
}

/* ---------- titlebar ---------- */

.titlebar {
    height: 40px;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: 10px;
    padding-left: 14px;
    background: var(--bg-elev);
    border-bottom: 1px solid var(--border);
    user-select: none;
    -webkit-user-select: none;
}

.titlebar-brand {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 14px;
    font-weight: 800;
    letter-spacing: -0.02em;
    color: var(--text);
}
.tb-logo {
    display: inline-flex;
    align-items: center;
}
.tb-logo .mark {
    width: 18px;
    height: 18px;
}

/* ---------- brand mark ----------
   Two raster variants ship (see logo.rs): the artwork is white + periwinkle,
   which vanishes on a pale background, so the light theme swaps in an
   ink-coloured version. Dark is the default; the light rule overrides it. */
.mark-light { display: none; }
.mark-dark  { display: block; }
[data-theme="light"] .mark-light { display: block; }
[data-theme="light"] .mark-dark  { display: none; }

.tb-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--muted);
    flex-shrink: 0;
    transition: background 0.2s, box-shadow 0.2s;
}
.tb-dot.live {
    background: var(--live);
    box-shadow: 0 0 6px var(--live);
    animation: live-glow 1.8s ease-in-out infinite;
}
@keyframes live-glow {
    0%, 100% { box-shadow: 0 0 4px var(--live); opacity: 1; }
    50% { box-shadow: 0 0 10px var(--live); opacity: 0.7; }
}

.titlebar-spacer { flex: 1; }

.titlebar-controls {
    display: flex;
    align-items: stretch;
    height: 100%;
    margin-left: 6px;
}
.tb-btn {
    width: 46px;
    height: 100%;
    border: none;
    background: none;
    color: var(--text-2);
    font-size: 14px;
    font-family: inherit;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: background 0.12s, color 0.12s;
}
.tb-btn:hover { background: var(--panel-2); color: var(--text); }
.tb-btn.close:hover { background: var(--danger); color: #fff; }

/* ---------- auth screen ---------- */

.auth {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
    position: relative;
    overflow: hidden;
    background: var(--bg);
}

/* SlicedWaves WebGL background (React Bits "SlicedWaves" port) fills the whole
   auth screen behind the card. The canvas is click-through; the card sits on
   top and stays interactive. */
.auth-waves {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    z-index: 0;
    pointer-events: none;
}

.auth-card {
    position: relative;
    z-index: 1;
    width: 360px;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 16px;
    padding: 32px;
    box-shadow: var(--shadow);
}

.auth-brand { margin-bottom: 22px; text-align: center; }
.auth-logo .mark {
    width: 56px;
    height: 56px;
    margin: 0 auto 12px;
    border-radius: 14px;
}

.brand {
    font-size: 30px;
    font-weight: 800;
    letter-spacing: -0.03em;
    color: var(--accent);
}

.brand-sub { margin: 4px 0 0; color: var(--muted); font-size: 13px; }

.auth h2 { margin: 0 0 18px; font-size: 18px; }

.field { margin-bottom: 14px; }
.field label {
    display: block;
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.08em;
    color: var(--muted);
    margin-bottom: 6px;
}

input[type="text"], input[type="password"], input[type="email"] {
    width: 100%;
    background: var(--input-bg);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 10px 12px;
    font-size: 14px;
    font-family: inherit;
    outline: none;
    transition: border-color 0.15s, box-shadow 0.15s;
}
input:focus {
    border-color: var(--border-strong);
    box-shadow: 0 0 0 3px var(--accent-soft);
}
input:disabled { opacity: 0.6; cursor: default; }
::placeholder { color: var(--muted); }

/* The native dropdown renders its own list; force option colors so the
   selected protocol keeps readable text in both themes. */
select {
    width: 100%;
    background: var(--input-bg);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 10px 12px;
    font-size: 14px;
    font-family: inherit;
    outline: none;
    cursor: pointer;
}
select:focus { border-color: var(--border-strong); }
select option {
    background: var(--bg);
    color: var(--text);
}

.btn {
    border: none;
    border-radius: 8px;
    cursor: pointer;
    font-family: inherit;
    font-weight: 600;
    font-size: 14px;
    padding: 10px 14px;
    transition: background 0.15s, transform 0.05s, opacity 0.15s;
}
.btn:active { transform: translateY(1px); }
.btn:disabled,
.btn.disabled {
    opacity: 0.45;
    cursor: not-allowed;
    filter: saturate(0.7);
    box-shadow: none;
    transform: none;
}

.btn-primary {
    width: 100%;
    background: var(--accent);
    color: var(--bg);
    padding: 12px;
    font-size: 15px;
    border-radius: 10px;
}
.btn-primary:hover { background: var(--accent-hover); }

.btn-ghost {
    background: var(--panel-2);
    color: var(--text-2);
    border: 1px solid var(--border);
}
.btn-ghost:hover { background: var(--card-hover); color: var(--text); }

.link {
    background: none;
    border: none;
    color: var(--text-2);
    cursor: pointer;
    font-size: 13px;
    padding: 0;
    margin-top: 12px;
    font-family: inherit;
}
.link:hover { color: var(--text); text-decoration: underline; }
.link:disabled { opacity: 0.6; cursor: default; }

.github-btn {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 10px;
    background: var(--panel-2);
    color: var(--text);
    border: 1px solid var(--border-strong);
    padding: 11px;
    border-radius: 10px;
    font-size: 15px;
    font-weight: 600;
}
.github-btn:hover:not(:disabled) { background: var(--card-hover); border-color: var(--text-2); }
.github-mark { display: inline-flex; }

.auth-divider {
    display: flex;
    align-items: center;
    gap: 12px;
    margin: 16px 0;
    color: var(--muted);
    font-size: 12px;
}
.auth-divider::before,
.auth-divider::after {
    content: "";
    flex: 1;
    height: 1px;
    background: var(--border);
}

.auth-error {
    margin-top: 12px;
    color: var(--danger);
    font-size: 13px;
    background: var(--danger-soft);
    border-radius: 8px;
    padding: 8px 10px;
}

/* ---------- profile menu ---------- */

.profile {
    position: relative;
    display: flex;
    align-items: center;
}
.profile-btn {
    width: 30px; height: 30px;
    border-radius: 50%;
    border: 1px solid var(--border-strong);
    background: var(--panel-2);
    color: var(--text-2);
    font-weight: 700;
    font-size: 12px;
    cursor: pointer;
    font-family: inherit;
    display: flex; align-items: center; justify-content: center;
}
.profile-btn:hover { border-color: var(--text-2); color: var(--text); }
.profile-avatar {
    width: 100%;
    height: 100%;
    border-radius: 50%;
    object-fit: cover;
    display: block;
}

.menu {
    position: absolute;
    right: 0;
    top: 36px;
    width: 200px;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 12px;
    box-shadow: var(--shadow);
    padding: 6px;
    z-index: 100;
    animation: menu-in 0.14s ease;
}
@keyframes menu-in {
    from { opacity: 0; transform: translateY(-4px) scale(0.98); }
    to { opacity: 1; transform: translateY(0) scale(1); }
}
.menu-label {
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.06em;
    color: var(--muted);
    padding: 8px 10px 4px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.menu-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    text-align: left;
    background: none;
    border: none;
    color: var(--text-2);
    font-size: 13px;
    font-family: inherit;
    padding: 8px 10px;
    border-radius: 8px;
    cursor: pointer;
}
.menu-item:hover { background: var(--card-hover); color: var(--text); }
.menu-item.danger { color: var(--danger); }
.menu-item.danger:hover { background: var(--danger-soft); }
.menu-sep { height: 1px; background: var(--border); margin: 6px 4px; }

/* ---------- layout ---------- */

.body {
    flex: 1;
    display: flex;
    min-height: 0;
}

.sidebar {
    width: 290px;
    flex-shrink: 0;
    border-right: 1px solid var(--border);
    padding: 20px;
    background: var(--bg-elev);
    overflow-y: auto;
}

.side-label {
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.1em;
    color: var(--muted);
    margin-bottom: 14px;
}

.side-label .hint {
    font-weight: 400;
    letter-spacing: 0;
    text-transform: none;
    color: var(--muted);
    margin-top: 8px;
    font-size: 12px;
    line-height: 1.5;
}

.btn-add {
    width: 100%;
    background: var(--accent);
    color: var(--bg);
    padding: 12px;
    font-size: 15px;
    font-weight: 700;
    border-radius: 10px;
    margin-bottom: 16px;
}
.btn-add:hover { background: var(--accent-hover); }

.btn-danger {
    background: var(--danger-soft);
    color: var(--danger);
    border: 1px solid var(--danger);
}
.btn-danger:hover { background: var(--danger); color: #fff; }

.btn-sm { padding: 6px 10px; font-size: 12px; }

.main {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-width: 0;
    overflow: hidden;
}

.tabs {
    display: flex;
    gap: 4px;
    padding: 12px 20px 0;
}
.tab {
    background: none;
    border: none;
    color: var(--muted);
    font-size: 14px;
    font-weight: 600;
    font-family: inherit;
    padding: 8px 14px;
    border-radius: 8px;
    cursor: pointer;
    transition: color 0.15s, background 0.15s;
}
.tab:hover { color: var(--text-2); }
.tab.active {
    color: var(--text);
    background: var(--panel-2);
    animation: tab-pop 0.18s ease;
}
@keyframes tab-pop {
    from { transform: scale(0.96); }
    to { transform: scale(1); }
}

.content {
    flex: 1;
    overflow-y: auto;
    padding: 20px;
}

/* ---------- panels ---------- */

.panel-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 16px;
}
.panel-title { font-size: 20px; font-weight: 700; margin: 0; }
.panel-count { color: var(--muted); font-size: 13px; }

.empty {
    text-align: center;
    color: var(--muted);
    padding: 60px 0;
}
.empty-icon {
    font-size: 48px;
    margin-bottom: 16px;
    opacity: 0.25;
}
.empty h3 { margin: 0 0 6px; font-size: 16px; color: var(--text-2); }
.empty p { margin: 0; font-size: 14px; }

/* ---------- tunnel cards ---------- */

.card {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 10px 14px;
    margin-bottom: 8px;
    display: flex;
    align-items: center;
    gap: 14px;
    cursor: pointer;
    transition: background 0.12s, border-color 0.12s, transform 0.12s;
    animation: rise-in 0.2s ease both;
}
.card:hover { background: var(--card-hover); border-color: var(--border-strong); transform: translateY(-1px); }
.card.open { border-color: var(--border-strong); background: var(--card-hover); }

.card-status { width: 56px; flex-shrink: 0; }
.card-status .live-badge,
.card-status .wait-badge {
    display: inline-block;
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.06em;
    padding: 2px 8px;
    border-radius: 999px;
    text-transform: uppercase;
}
.card-status .live-badge {
    color: var(--live);
    background: rgba(204, 204, 255, 0.12);
    border: 1px solid var(--live);
}
.card-status .wait-badge {
    color: var(--muted);
    border: 1px solid var(--border);
}

.card-main { flex: 1; min-width: 0; }
.card-url {
    font-family: var(--mono);
    font-size: 13px;
    color: var(--text);
    word-break: break-all;
}
.card-sub {
    display: flex;
    gap: 12px;
    margin-top: 3px;
    font-size: 12px;
    color: var(--muted);
    align-items: center;
}
.card-sub .proto {
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.06em;
    color: var(--text-2);
    background: var(--panel-2);
    border: 1px solid var(--border);
    padding: 1px 6px;
    border-radius: 4px;
}
.card-sub .local { font-family: var(--mono); }

.card-meta {
    text-align: right;
    flex-shrink: 0;
}
.card-meta .reqs { font-size: 12px; color: var(--text-2); font-weight: 600; }
.card-meta .ends { font-size: 11px; color: var(--muted); margin-top: 2px; }

.icon-btn {
    background: var(--panel-2);
    border: 1px solid var(--border);
    color: var(--text-2);
    border-radius: 7px;
    width: 30px; height: 30px;
    cursor: pointer;
    font-size: 12px;
    font-family: inherit;
    flex-shrink: 0;
}
.icon-btn:hover { color: var(--text); border-color: var(--border-strong); }
.icon-btn.danger:hover { color: var(--danger); border-color: var(--danger); background: var(--danger-soft); }
.icon-btn svg {
    display: block;
    margin: 0 auto;
}

/* ---------- modals ---------- */

.modal-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.45);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 500;
    padding: 24px;
}
[data-theme="light"] .modal-overlay { background: rgba(0, 0, 0, 0.18); }

.modal {
    background: var(--panel);
    border: 1px solid var(--border-strong);
    border-radius: 14px;
    box-shadow: var(--shadow);
    padding: 22px 24px;
    width: 100%;
    max-width: 420px;
    max-height: calc(100vh - 96px);
    overflow-y: auto;
    animation: modal-in 0.16s ease;
}
@keyframes modal-in {
    from { opacity: 0; transform: translateY(8px) scale(0.985); }
    to { opacity: 1; transform: translateY(0) scale(1); }
}

.modal-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 18px;
}.modal-title { font-size: 17px; font-weight: 700; }
.modal-x {
    background: none;
    border: none;
    color: var(--muted);
    font-size: 15px;
    cursor: pointer;
    width: 28px; height: 28px;
    border-radius: 7px;
    font-family: inherit;
}
.modal-x:hover { background: var(--panel-2); color: var(--text); }

.modal-actions {
    display: flex;
    gap: 10px;
    justify-content: flex-end;
    margin-top: 22px;
}
.modal-actions .btn { flex: 1; }

/* ---------- about modal ---------- */

.about { max-width: 360px; text-align: center; }
.about-body { padding: 8px 4px 4px; }
.about-logo .mark {
    width: 64px;
    height: 64px;
    margin: 0 auto 12px;
    border-radius: 16px;
}
.about-name { font-size: 22px; font-weight: 800; letter-spacing: -0.02em; }
.about-ver { margin: 2px 0 14px; font-size: 12px; color: var(--muted); font-family: var(--mono); }
.about-desc { font-size: 13px; line-height: 1.6; color: var(--text-2); }
.about-links {
    display: flex;
    gap: 10px;
    justify-content: center;
    margin-top: 18px;
}
.about-foot { margin-top: 20px; padding-top: 12px; border-top: 1px solid var(--border); font-size: 12px; color: var(--muted); }

/* ---------- segmented control ---------- */

.seg {
    display: flex;
    background: var(--input-bg);
    border: 1px solid var(--border);
    border-radius: 9px;
    padding: 3px;
    gap: 3px;
}
.seg-btn {
    flex: 1;
    background: none;
    border: none;
    color: var(--text-2);
    font-size: 13px;
    font-weight: 600;
    font-family: inherit;
    padding: 8px 10px;
    border-radius: 7px;
    cursor: pointer;
    white-space: nowrap;
}
.seg-btn:hover { color: var(--text); }
.seg-btn.active {
    background: var(--panel-2);
    color: var(--text);
    box-shadow: inset 0 0 0 1px var(--border-strong);
}

/* ---------- settings ---------- */

.settings-section { margin-bottom: 24px; }
.settings-section:last-child { margin-bottom: 0; }
.settings-avatar {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 64px;
    height: 64px;
    margin: 0 auto 14px;
    border-radius: 50%;
    background: var(--accent-soft);
    border: 2px solid var(--border);
    color: var(--text);
    font-size: 24px;
    font-weight: 700;
    overflow: hidden;
}
.settings-avatar img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    border-radius: 50%;
    display: block;
}
.section-title {
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted);
    margin-bottom: 10px;
}
.setting-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 9px 0;
    border-top: 1px solid var(--border);
}
.setting-lbl { font-size: 13px; color: var(--text-2); }
.setting-val {
    font-size: 13px;
    color: var(--text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: var(--mono);
}

/* ---------- subdomain input ---------- */

.sub-input {
    display: flex;
    align-items: stretch;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--input-bg);
    overflow: hidden;
}
.sub-input:focus-within {
    border-color: var(--border-strong);
    box-shadow: 0 0 0 3px var(--accent-soft);
}
.sub-input input {
    flex: 1;
    border: none;
    box-shadow: none;
    min-width: 0;
}
.sub-input .sub-suffix {
    display: flex;
    align-items: center;
    padding: 0 12px;
    font-size: 13px;
    font-family: var(--mono);
    color: var(--muted);
    background: var(--panel-2);
    border-left: 1px solid var(--border);
    white-space: nowrap;
}
.sub-input input:focus { box-shadow: none; }

.field .hint {
    font-size: 12px;
    color: var(--muted);
    margin-top: 6px;
    line-height: 1.45;
}
.field .hint.warn {
    color: var(--danger);
    font-weight: 600;
}

.card-detail {
    grid-column: 1 / -1;
    border-top: 1px solid var(--border);
    margin-top: 10px;
    padding-top: 12px;
}
.detail-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 12px 16px;
}
.detail-item { min-width: 0; }
.detail-label {
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted);
    margin-bottom: 3px;
}
.detail-value {
    font-size: 13px;
    color: var(--text);
    word-break: break-all;
}
.detail-value.mono { font-family: var(--mono); }

.card-open-url {
    margin-top: 12px;
}
.card-open-url a {
    color: var(--text-2);
    font-size: 13px;
    cursor: pointer;
    text-decoration: none;
}
.card-open-url a:hover { color: var(--text); text-decoration: underline; }

/* ---------- usage ---------- */

.chart-card {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 14px 16px;
    margin-bottom: 16px;
    animation: rise-in 0.2s ease both;
}
.chart-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 8px;
}
.chart-title-wrap {
    display: flex;
    align-items: baseline;
    gap: 8px;
    min-width: 0;
}
.chart-title { font-size: 13px; font-weight: 700; color: var(--text-2); white-space: nowrap; }
.chart-scope-name {
    font-size: 12px;
    font-family: var(--mono);
    color: var(--live);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.chart-scope {
    flex-shrink: 0;
    width: auto;
    min-width: 150px;
    padding: 4px 8px;
    font-size: 12px;
    border-radius: 7px;
    cursor: pointer;
    color: var(--text);
}
.chart-rate {
    font-size: 11px;
    color: var(--muted);
    font-family: var(--mono);
    margin-bottom: 6px;
}
.chart {
    display: block;
    width: 100%;
    height: 120px;
}
.chart-area { animation: chart-fade 0.35s ease both; }
.chart-line {
    stroke-linejoin: round;
    stroke-linecap: round;
    animation: chart-draw 0.45s ease-out both;
}
.chart-live-dot {
    fill: var(--live);
    animation: dot-pulse 1.6s ease-in-out infinite;
    transform-origin: center;
    transform-box: fill-box;
}
.chart-empty {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 120px;
    color: var(--muted);
    font-size: 13px;
}
.chart-card.chart-empty { padding: 0; }

.chart-grid {
    stroke: var(--border);
    stroke-width: 1;
    stroke-dasharray: 2 4;
    vector-effect: non-scaling-stroke;
}
.chart-ylbl {
    fill: var(--muted);
    font-size: 9px;
    font-family: var(--mono);
}
.chart-xlbl {
    fill: var(--muted);
    font-size: 9px;
    font-family: var(--mono);
}
.chart-nodata {
    fill: var(--muted);
    font-size: 13px;
    font-weight: 600;
    letter-spacing: 0.02em;
}

.chart-legend {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 12px;
    margin-top: 8px;
    padding-top: 8px;
    border-top: 1px solid var(--border);
}
.legend-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    font-family: var(--mono);
    color: var(--text-2);
    padding: 2px 8px 2px 6px;
    border: 1px solid var(--border);
    border-radius: 999px;
    cursor: pointer;
    background: var(--panel-2);
    transition: border-color 0.12s, color 0.12s;
}
.legend-chip:hover { border-color: var(--border-strong); color: var(--text); }
.legend-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
    display: inline-block;
}

/* ---------- chart containers (React + Recharts) ---------- */

.rust-chart { width: 100%; }
/* Recharts is handed explicit pixel dimensions measured from the container, so
   every chart div needs a definite CSS height. Without one the engine measures
   a zero-height box, skips the render and retries — the chart never appears. */
#realtime-chart { height: 170px; }
#insight-area { height: 140px; }
#insight-pie, #insight-radar, #insight-radial { height: 210px; }
.rust-chart .recharts-surface { overflow: visible; }

/* ---------- toggle switch ---------- */

.switch {
    width: 36px;
    height: 20px;
    border-radius: 999px;
    background: var(--panel-2);
    border: 1px solid var(--border-strong);
    cursor: pointer;
    position: relative;
    transition: background 0.18s, border-color 0.18s;
    flex-shrink: 0;
}
.switch.on {
    background: var(--live);
    border-color: var(--live);
}
.switch-knob {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: var(--muted);
    transition: left 0.18s ease, background 0.18s;
}
.switch.on .switch-knob {
    left: 18px;
    background: #ffffff;
}
.card-toggle { flex-shrink: 0; display: flex; align-items: center; }

/* ---------- paused state ---------- */

.paused-badge {
    display: inline-block;
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.06em;
    padding: 2px 8px;
    border-radius: 999px;
    text-transform: uppercase;
    color: var(--text-2);
    background: var(--panel-2);
    border: 1px solid var(--border-strong);
}
.card.paused {
    opacity: 0.72;
    background: var(--panel);
}

/* ---------- insight modal ---------- */

.modal.insight {
    width: min(960px, calc(100vw - 80px));
    max-width: 960px;
}
.insight-status {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 12px;
}
.insight-url {
    font-family: var(--mono);
    font-size: 12px;
    color: var(--muted);
}
.insight-charts { margin-bottom: 14px; }
.insight-chart {
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 8px 10px;
    margin-bottom: 12px;
}
.insight-chart-title {
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
    margin-bottom: 4px;
}
.insight-chart-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
}
.insight-chart-card {
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 8px;
    min-width: 0;
}
.insight-metrics {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px 14px;
    margin-bottom: 18px;
}
.metric { min-width: 0; }
.metric-lbl {
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted);
    margin-bottom: 3px;
}
.metric-val {
    font-size: 15px;
    font-weight: 700;
    color: var(--text);
    font-family: var(--mono);
}
.toggle-wrap {
    display: inline-flex;
    align-items: center;
    gap: 8px;
}
.switch-lbl {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-2);
    font-family: inherit;
}
.insight-log {
    max-height: 220px;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 10px 12px;
    background: var(--panel-2);
}
.insight-log .section-title { margin-bottom: 8px; }
.insight-log-empty {
    font-size: 12px;
    color: var(--muted);
    padding: 6px 0;
}
.log-row {
    display: flex;
    align-items: baseline;
    gap: 10px;
    padding: 4px 0;
    border-top: 1px solid var(--border);
    font-size: 12px;
}
.log-row:first-child { border-top: none; }
.log-time { color: var(--muted); font-family: var(--mono); font-size: 11px; flex-shrink: 0; }
.log-kind {
    color: var(--text-2);
    font-family: var(--mono);
    font-size: 11px;
    text-transform: lowercase;
    flex-shrink: 0;
}
.log-payload { color: var(--muted); font-family: var(--mono); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* ---------- empty state CTA ---------- */

.empty-cta { display: flex; flex-direction: column; align-items: center; gap: 14px; }
.empty-cta .btn { align-self: center; min-width: 200px; }

@keyframes rise-in {
    from { opacity: 0; transform: translateY(6px); }
    to { opacity: 1; transform: translateY(0); }
}
@keyframes chart-draw {
    from { opacity: 0; }
    to { opacity: 1; }
}
@keyframes chart-fade {
    from { opacity: 0; }
    to { opacity: 1; }
}
@keyframes dot-pulse {
    0%, 100% { opacity: 1; transform: scale(1); }
    50% { opacity: 0.45; transform: scale(1.6); }
}

.stats-row {
    display: flex;
    gap: 12px;
    margin-bottom: 20px;
}
.stat-card {
    flex: 1;
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 14px 16px;
    animation: rise-in 0.2s ease both;
}
.stat-card:nth-child(2) { animation-delay: 0.04s; }
.stat-card:nth-child(3) { animation-delay: 0.08s; }
.stat-card:nth-child(4) { animation-delay: 0.12s; }
.stat-card .num { font-size: 22px; font-weight: 800; }
.stat-card .lbl { font-size: 12px; color: var(--muted); margin-top: 2px; }

.usage-table {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 10px;
    overflow: hidden;
}
.usage-head,
.usage-row {
    display: grid;
    grid-template-columns: 2fr 1fr 1fr 1fr auto;
    gap: 16px;
    align-items: center;
    padding: 10px 16px;
}
.usage-head {
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted);
    background: var(--panel-2);
    border-bottom: 1px solid var(--border);
}
.usage-row {
    font-size: 13px;
    border-bottom: 1px solid var(--border);
    cursor: pointer;
    transition: background 0.12s;
}
.usage-row:last-child { border-bottom: none; }
.usage-row:hover { background: var(--card-hover); }
.usage-row.scope { background: var(--accent-soft); box-shadow: inset 3px 0 0 var(--live); }

.usage-cell.name { min-width: 0; }
.usage-sub {
    color: var(--text);
    font-family: var(--mono);
    font-size: 13px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.usage-local {
    color: var(--muted);
    font-size: 11px;
    font-family: var(--mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.mini-label {
    font-size: 12px;
    color: var(--text-2);
    font-family: var(--mono);
    margin-bottom: 4px;
}
.mini-bar {
    height: 5px;
    background: var(--panel-2);
    border-radius: 999px;
    overflow: hidden;
}
.mini-fill {
    height: 100%;
    background: var(--text-2);
    border-radius: 999px;
}
.mini-fill.in { background: var(--info); }
.mini-fill.out { background: var(--live); }

.usage-cell.actions { justify-self: end; }
.insight-btn {
    font-size: 11px;
    font-weight: 600;
    color: var(--text-2);
    background: var(--panel-2);
    border: 1px solid var(--border-strong);
    border-radius: 6px;
    padding: 4px 10px;
    cursor: pointer;
    white-space: nowrap;
    transition: border-color 0.12s, color 0.12s, background 0.12s;
}
.insight-btn:hover {
    border-color: var(--live);
    color: var(--live);
    background: var(--panel);
}

/* ---------- toasts ---------- */

.toasts {
    position: fixed;
    bottom: 20px;
    left: 20px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    z-index: 1000;
    pointer-events: none;
}
.toast {
    display: flex;
    align-items: center;
    gap: 10px;
    background: var(--panel);
    color: var(--text);
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    padding: 10px 14px;
    font-size: 13px;
    box-shadow: var(--shadow);
    max-width: 340px;
    animation: toast-in 0.2s ease;
}
.toast .ic { font-size: 14px; }
.toast.success .ic { color: var(--live); }
.toast.error .ic { color: var(--danger); }
.toast.info .ic { color: var(--info); }

@keyframes toast-in {
    from { opacity: 0; transform: translateY(6px); }
    to { opacity: 1; transform: translateY(0); }
}

::-webkit-scrollbar { width: 10px; }
::-webkit-scrollbar-thumb { background: var(--border-strong); border-radius: 5px; }
::-webkit-scrollbar-track { background: transparent; }

/* ---------- responsive ---------- */

.card { flex-wrap: wrap; }
.card-main { min-width: 0; }

.detail-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
}

.stats-row { flex-wrap: wrap; }
.stat-card { min-width: 150px; }

@media (max-width: 1180px) {
    .detail-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .insight-metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}

@media (max-width: 960px) {
    .sidebar { width: 220px; }
    .usage-table {
        overflow-x: auto;
        -webkit-overflow-scrolling: touch;
    }
    .usage-head, .usage-row {
        grid-template-columns: minmax(160px, 2fr) 1fr 1fr 1fr auto;
        min-width: 640px;
    }
    .insight-chart-grid { grid-template-columns: 1fr; }
    .card-meta { text-align: left; }
}

@media (max-width: 720px) {
    .sidebar { display: none; }
    .body { flex-direction: column; }
    .main { overflow-y: auto; }
    .content { padding: 14px; }
    .tabs { padding: 10px 14px 0; }
    .detail-grid { grid-template-columns: 1fr; }
    .insight-metrics { grid-template-columns: 1fr; }
    .stats-row { flex-direction: column; }
    .stat-card { min-width: 0; }
    .auth-card { width: 100%; max-width: 360px; }
    .modal { max-width: 100%; }
}
"#
}
