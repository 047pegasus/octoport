#!/usr/bin/env bash
# OctoPort installer — fetches the latest platform binary/package and installs it.
# Binaries/packages are served from the project's GitHub releases.
set -euo pipefail

# Default: the OctoPort project's GitHub releases. Override to host binaries
# elsewhere, e.g. a fork:
#   OCTOPORT_REPO=https://github.com/yourorg/octoport OCTOPORT_VERSION=v0.1.0 ./install.sh
# Release assets follow the naming convention:
#   CLI: octoport-<os>-<arch> (raw binary), octoport-<os>-<arch>.tar.gz, .deb, .rpm, .msi
#   GUI: octoport-app-<os>-<arch> (raw binary), .tar.gz, .AppImage, .dmg, .pkg, .msi
REPO="${OCTOPORT_REPO:-https://github.com/047pegasus/octoport}"
VERSION="${OCTOPORT_VERSION:-latest}"
DEST_DIR="${OCTOPORT_INSTALL_DIR:-/usr/local/bin}"
INSTALL_CLI="${OCTOPORT_INSTALL_CLI:-true}"
INSTALL_GUI="${OCTOPORT_INSTALL_GUI:-true}"

usage() {
  cat <<'EOF'
Usage: ./install.sh [OPTIONS]

Options:
  --cli-only       Install only the CLI
  --gui-only       Install only the GUI
  --both           Install both CLI and GUI (default)
  --uninstall      Uninstall OctoPort (CLI, GUI, or both)
  --version VER    Version to install (default: latest)
  --repo URL       GitHub repo URL (default: https://github.com/047pegasus/octoport)
  --dest DIR       Install directory for CLI binary (default: /usr/local/bin)
  -h, --help       Show this help

Environment variables:
  OCTOPORT_VERSION       Version tag (e.g. v0.2.0) or "latest"
  OCTOPORT_REPO          GitHub repository URL
  OCTOPORT_INSTALL_DIR   CLI install directory
  OCTOPORT_INSTALL_CLI   "true"/"false" (default: true)
  OCTOPORT_INSTALL_GUI   "true"/"false" (default: true)
EOF
  exit 0
}

# Parse args
UNINSTALL=false
while [[ $# -gt 0 ]]; do
  case $1 in
    --cli-only) INSTALL_CLI=true; INSTALL_GUI=false; shift ;;
    --gui-only) INSTALL_CLI=false; INSTALL_GUI=true; shift ;;
    --both) INSTALL_CLI=true; INSTALL_GUI=true; shift ;;
    --uninstall) UNINSTALL=true; shift ;;
    --version) VERSION="$2"; shift 2 ;;
    --repo) REPO="$2"; shift 2 ;;
    --dest) DEST_DIR="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "Unknown option: $1"; usage ;;
  esac
done

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "x86_64" ;;
    aarch64|arm64) echo "aarch64" ;;
    *) echo "unsupported-arch"; exit 1 ;;
  esac
}

detect_os() {
  case "$(uname -s)" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "macos" ;;
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
    *) echo "unsupported-os"; exit 1 ;;
  esac
}

OS="$(detect_os)"
ARCH="$(detect_arch)"

# --- progress UI -------------------------------------------------------------
# ONE persistent spinner runs for the entire install: it stays pinned to the
# bottom line while every log message, download and install step executes, so
# the script never looks stalled. The only thing that pauses it is the curl
# progress bar (downloads), which draws its own percentage. When stdout isn't a
# terminal (piped output) the spinner silently disables itself.
SPINNER_FRAMES=("⠋" "⠙" "⠹" "⠸" "⠼" "⠴" "⠦" "⠧" "⠇" "⠏")
SPINNER_PID=""
SPINNER_LABEL_FILE=""
SPINNER_PAUSE_FILE=""

# Launch the persistent spinner. The label is read from a small file each frame
# so `spinner_label` can update it live. A pause-flag file makes the loop skip
# drawing (used by `say`, `warn`, `sudo` and `download_asset` so the terminal
# line is free for messages, password prompts and the curl progress bar).
spinner_start() {
  if [ ! -t 1 ]; then
    return
  fi
  SPINNER_LABEL_FILE="$(mktemp)"
  SPINNER_PAUSE_FILE="$(mktemp)"
  rm -f "$SPINNER_PAUSE_FILE"
  printf '%s' "${1:-working}" > "$SPINNER_LABEL_FILE"
  (
    local i=0
    while :; do
      if [ -f "$SPINNER_PAUSE_FILE" ]; then
        sleep 0.1
        continue
      fi
      local g="${SPINNER_FRAMES[$((i % ${#SPINNER_FRAMES[@]}))]}"
      local l
      l="$(cat "$SPINNER_LABEL_FILE" 2>/dev/null || echo working)"
      printf '\r\033[K  %s %s…' "$g" "$l"
      i=$((i + 1))
      sleep 0.1
    done
  ) &
  SPINNER_PID=$!
}

# Update the persistent spinner's label (e.g. "installing CLI binary").
spinner_label() {
  if [ -n "$SPINNER_LABEL_FILE" ]; then
    printf '%s' "$1" > "$SPINNER_LABEL_FILE" 2>/dev/null || true
  fi
}

# Pause drawing: create the flag file and wait one frame so the loop has
# observed it and is no longer writing to the terminal. Never signals the
# spinner process, so it cannot deadlock.
spinner_pause() {
  if [ -n "$SPINNER_PAUSE_FILE" ]; then
    : > "$SPINNER_PAUSE_FILE"
    sleep 0.15
  fi
}

# Resume drawing after a `spinner_pause`.
spinner_resume() {
  if [ -n "$SPINNER_PAUSE_FILE" ]; then
    rm -f "$SPINNER_PAUSE_FILE"
  fi
}

# Stop the spinner, clear its line and drop its support files. Idempotent.
spinner_stop() {
  if [ -n "$SPINNER_PID" ]; then
    kill "$SPINNER_PID" 2>/dev/null || true
    wait "$SPINNER_PID" 2>/dev/null || true
    SPINNER_PID=""
  fi
  rm -f "$SPINNER_LABEL_FILE" "$SPINNER_PAUSE_FILE" 2>/dev/null || true
  SPINNER_LABEL_FILE=""
  SPINNER_PAUSE_FILE=""
  printf '\r\033[2K' || true
}

# Print a log line above the spinner: pause drawing so the spinner isn't
# mid-frame, clear the status line, write the message with a trailing newline,
# then resume so the spinner re-draws on the new bottom line.
say() {
  if [ -t 1 ] && [ -n "$SPINNER_PID" ]; then
    spinner_pause
    printf '\r\033[2K%s\n' "$*"
    spinner_resume
  else
    printf '%s\n' "$*"
  fi
}

# Same as `say` but routed to stderr (for errors / warnings).
warn() {
  if [ -t 1 ] && [ -n "$SPINNER_PID" ]; then
    spinner_pause
    printf '\r\033[2K%s\n' "$*" >&2
    spinner_resume
  else
    printf '%s\n' "$*" >&2
  fi
}

# Run a privileged command while keeping the spinner animating. Only when sudo
# actually needs a password (detected with -n) is the spinner paused long
# enough to prompt and cache credentials; the command itself then runs with the
# spinner turning, so long apt/dnf steps never look frozen.
sudo() {
  if command sudo -n true 2>/dev/null; then
    command sudo "$@"
  else
    spinner_pause
    command sudo -v
    spinner_resume
    command sudo "$@"
  fi
}

# Download with curl's own percentage bar. The spinner is paused for the whole
# transfer so the bar renders cleanly, then resumed. A generous max-time stops
# a dead network from hanging the install forever.
download_asset() {
  local url="$1" out="$2"
  if [ -t 1 ]; then
    spinner_pause
    spinner_label "downloading $(basename "$out")"
    curl -fSL --connect-timeout 15 --max-time 600 --progress-bar "$url" -o "$out"
    local rc=$?
    spinner_label "downloaded $(basename "$out")"
    spinner_resume
    return $rc
  else
    curl -fSL --connect-timeout 15 --max-time 600 "$url" -o "$out"
  fi
}

# Run an install step under the persistent spinner: just retitles the status
# line and runs the command in the foreground while the spinner keeps turning.
# The command's stdout is hidden so its output doesn't fight the spinner; errors
# (stderr) still surface. The return code is preserved for `set -e`.
run_with_spinner() {
  local label="$1"
  shift
  spinner_label "$label"
  "$@" >/dev/null
}

# Version path
if [ "$VERSION" = "latest" ]; then
  BASE_URL="$REPO/releases/latest/download"
  SHA256SUMS_URL="$REPO/releases/latest/download/SHA256SUMS"
else
  BASE_URL="$REPO/releases/download/$VERSION"
  SHA256SUMS_URL="$REPO/releases/download/$VERSION/SHA256SUMS"
fi

# The persistent spinner runs for the rest of the script, and is torn down on
# any exit path (success, error, or interrupt) so no background process is left
# behind and the terminal isn't left mid-redraw.
spinner_start "initializing"
trap 'spinner_stop' EXIT

# Uninstall logic
if [ "$UNINSTALL" = "true" ]; then
  say "Uninstalling OctoPort..."

  # CLI binary
  if [ "$INSTALL_CLI" = "true" ]; then
    say "Removing CLI..."
    sudo rm -f /usr/local/bin/octoport
    # Debian/Ubuntu
    sudo apt-get remove --purge octoport 2>/dev/null || true
    # RHEL/Fedora
    sudo rpm -e octoport 2>/dev/null || true
    # macOS
    sudo pkgutil --forget io.octoport.cli 2>/dev/null || true
    sudo rm -rf /usr/local/bin/octoport /Applications/OctoPort.app 2>/dev/null || true
    say "  CLI removed"
  fi

  # GUI
  if [ "$INSTALL_GUI" = "true" ]; then
    say "Removing GUI..."
    # Linux
    sudo apt-get remove --purge octoport-app 2>/dev/null || true
    sudo rpm -e octoport-app 2>/dev/null || true
    # macOS
    sudo pkgutil --forget io.octoport.gui 2>/dev/null || true
    sudo rm -rf /Applications/OctoPort.app /opt/octoport-app.AppImage /usr/local/bin/octoport-app 2>/dev/null || true
    # Windows (via winget/scoop if available, but we just clean up)
    say "  GUI removed"
  fi

  # Config directories
  rm -rf ~/.config/octoport ~/.config/octoport-app
  rm -rf ~/.local/share/octoport ~/.local/share/octoport-app

  say "OctoPort uninstalled."
  exit 0
fi

say "Installing OctoPort ${VERSION} ($OS/$ARCH) from $REPO"

TMP="$(mktemp -d)"
trap 'spinner_stop; rm -rf "$TMP"' EXIT

# Fetch SHA256SUMS once
say "Fetching checksums..."
if ! download_asset "$SHA256SUMS_URL" "$TMP/SHA256SUMS"; then
  warn "! could not fetch SHA256SUMS; aborting"
  exit 1
fi

# Verify checksum function
verify_and_install_binary() {
  local asset_name="$1"
  local dest_path="$2"
  local expected_checksum

  expected_checksum=$(grep " ${asset_name}\$" "$TMP/SHA256SUMS" | awk '{print $1}')
  if [ -z "$expected_checksum" ]; then
    warn "! no checksum published for ${asset_name}; aborting"
    exit 1
  fi

  local url="${BASE_URL}/${asset_name}"
  say "Downloading ${asset_name}..."
  download_asset "$url" "$TMP/${asset_name}"

  # Verify checksum
  local actual_checksum
  if command -v sha256sum >/dev/null 2>&1; then
    actual_checksum=$(sha256sum "$TMP/${asset_name}" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual_checksum=$(shasum -a 256 "$TMP/${asset_name}" | awk '{print $1}')
  else
    warn "! neither sha256sum nor shasum found; cannot verify download"
    exit 1
  fi

  if [ "$expected_checksum" != "$actual_checksum" ]; then
    warn "! checksum mismatch for ${asset_name} - refusing to install"
    warn "  expected $expected_checksum"
    warn "  actual   $actual_checksum"
    exit 1
  fi
  say "  checksum ok"

  # Install binary
  if [ ! -w "$(dirname "$dest_path")" ]; then
    warn "! $dest_path requires elevated privileges; using sudo"
    run_with_spinner "installing ${asset_name}" sudo install -m 755 "$TMP/${asset_name}" "$dest_path"
  else
    run_with_spinner "installing ${asset_name}" install -m 755 "$TMP/${asset_name}" "$dest_path"
  fi
}

install_deb() {
  local asset="$1"
  say "Installing .deb package: $asset"
  run_with_spinner "updating package lists" sudo apt-get update -qq && run_with_spinner "installing $asset" sudo apt-get install -y "$TMP/$asset"
}

install_rpm() {
  local asset="$1"
  say "Installing .rpm package: $asset"
  run_with_spinner "installing $asset" sudo dnf install -y "$TMP/$asset" 2>/dev/null || run_with_spinner "installing $asset" sudo yum install -y "$TMP/$asset"
}

install_dmg() {
  local asset="$1"
  say "Mounting and installing .dmg: $asset"
  local mount_point=$(hdiutil attach "$TMP/$asset" -nobrowse -quiet | tail -1 | awk '{print $3}')
  if [ -z "$mount_point" ]; then
    warn "! failed to mount DMG"
    exit 1
  fi
  # Find .app or .pkg inside
  if find "$mount_point" -name "*.app" -maxdepth 1 | grep -q .; then
    local app_path=$(find "$mount_point" -name "*.app" -maxdepth 1 | head -1)
    say "Installing .app to /Applications..."
    run_with_spinner "installing .app" sudo cp -R "$app_path" /Applications/
  elif find "$mount_point" -name "*.pkg" -maxdepth 1 | grep -q .; then
    local pkg_path=$(find "$mount_point" -name "*.pkg" -maxdepth 1 | head -1)
    say "Installing .pkg..."
    run_with_spinner "installing .pkg" sudo installer -pkg "$pkg_path" -target /
  fi
  hdiutil detach "$mount_point" -quiet
}

install_pkg() {
  local asset="$1"
  say "Installing .pkg: $asset"
  run_with_spinner "installing $asset" sudo installer -pkg "$TMP/$asset" -target /
}

install_msi() {
  local asset="$1"
  say "Installing .msi: $asset"
  run_with_spinner "installing $asset" msiexec /i "$TMP/$asset" /quiet /norestart
}

install_appimage() {
  local asset="$1"
  local dest="/opt/octoport-app.AppImage"
  say "Installing AppImage to $dest..."
  if [ ! -w "/opt" ]; then
    run_with_spinner "installing AppImage" sudo install -m 755 "$TMP/$asset" "$dest"
    run_with_spinner "linking octoport-app" sudo ln -sf "$dest" /usr/local/bin/octoport-app
  else
    run_with_spinner "installing AppImage" install -m 755 "$TMP/$asset" "$dest"
    run_with_spinner "linking octoport-app" ln -sf "$dest" /usr/local/bin/octoport-app
  fi

  # Register the app with the desktop environment: extract the desktop entry
  # and icon from the AppImage so it appears in the application menu and shows
  # the app icon in the taskbar (StartupWMClass matches octoport-app).
  (cd "$TMP" && "$TMP/$asset" --appimage-extract >/dev/null 2>&1)
  if [ ! -f "$TMP/squashfs-root/usr/share/applications/octoport-app.desktop" ]; then
    warn "! could not extract app menu entry from AppImage; app will not be registered"
    return
  fi
  if [ ! -w "/usr/share" ]; then
    run_with_spinner "registering app menu entry" sudo install -m 644 "$TMP/squashfs-root/usr/share/applications/octoport-app.desktop" /usr/share/applications/octoport-app.desktop
    sudo install -m 644 "$TMP/squashfs-root/usr/share/icons/hicolor/256x256/apps/octoport-app.png" /usr/share/icons/hicolor/256x256/apps/octoport-app.png
    sudo update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
  else
    run_with_spinner "registering app menu entry" install -m 644 "$TMP/squashfs-root/usr/share/applications/octoport-app.desktop" /usr/share/applications/octoport-app.desktop
    install -m 644 "$TMP/squashfs-root/usr/share/icons/hicolor/256x256/apps/octoport-app.png" /usr/share/icons/hicolor/256x256/apps/octoport-app.png
    update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
  fi
}

# Main install logic
if [ "$INSTALL_CLI" = "true" ]; then
  say "=== Installing CLI ==="
  case "$OS" in
    linux)
      # Prefer .deb/.rpm on supported distros, fallback to tar.gz
      if command -v dpkg >/dev/null 2>&1; then
        ASSET="octoport-${OS}-${ARCH}.deb"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
          install_deb "$ASSET"
        else
          ASSET="octoport-${OS}-${ARCH}.tar.gz"
          download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
          run_with_spinner "extracting ${ASSET}" tar xzf "$TMP/${ASSET}" -C "$TMP"
          verify_and_install_binary "octoport-${OS}-${ARCH}" "/usr/local/bin/octoport"
        fi
      elif command -v rpm >/dev/null 2>&1 || command -v dnf >/dev/null 2>&1; then
        ASSET="octoport-${OS}-${ARCH}.rpm"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
          install_rpm "$ASSET"
        else
          ASSET="octoport-${OS}-${ARCH}.tar.gz"
          download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
          run_with_spinner "extracting ${ASSET}" tar xzf "$TMP/${ASSET}" -C "$TMP"
          verify_and_install_binary "octoport-${OS}-${ARCH}" "/usr/local/bin/octoport"
        fi
      else
        ASSET="octoport-${OS}-${ARCH}.tar.gz"
        download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
        run_with_spinner "extracting ${ASSET}" tar xzf "$TMP/${ASSET}" -C "$TMP"
        verify_and_install_binary "octoport-${OS}-${ARCH}" "$DEST_DIR/octoport"
      fi
      ;;
    macos)
      ASSET="octoport-${OS}-${ARCH}.pkg"
      if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
        download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
        install_pkg "$ASSET"
      else
        ASSET="octoport-${OS}-${ARCH}"
        verify_and_install_binary "$ASSET" "$DEST_DIR/octoport"
      fi
      ;;
    windows)
      ASSET="octoport-${OS}-${ARCH}.msi"
      if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
        download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
        install_msi "$ASSET"
      else
        ASSET="octoport-${OS}-${ARCH}.exe"
        verify_and_install_binary "$ASSET" "$DEST_DIR/octoport.exe"
      fi
      ;;
  esac
fi

if [ "$INSTALL_GUI" = "true" ]; then
  say "=== Installing GUI ==="
  case "$OS" in
    linux)
      # Prefer AppImage or deb/rpm
      if command -v dpkg >/dev/null 2>&1; then
        ASSET="octoport-app-${OS}-${ARCH}.deb"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
          install_deb "$ASSET"
        else
          ASSET="octoport-app-${OS}-${ARCH}.AppImage"
          if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
            download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
            install_appimage "$ASSET"
          fi
        fi
      elif command -v rpm >/dev/null 2>&1 || command -v dnf >/dev/null 2>&1; then
        ASSET="octoport-app-${OS}-${ARCH}.rpm"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
          install_rpm "$ASSET"
        else
          ASSET="octoport-app-${OS}-${ARCH}.AppImage"
          if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
            download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
            install_appimage "$ASSET"
          fi
        fi
      else
        ASSET="octoport-app-${OS}-${ARCH}.AppImage"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
          install_appimage "$ASSET"
        else
          ASSET="octoport-app-${OS}-${ARCH}.tar.gz"
          download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
          run_with_spinner "extracting ${ASSET}" tar xzf "$TMP/${ASSET}" -C "$TMP"
          verify_and_install_binary "octoport-app-${OS}-${ARCH}" "/usr/local/bin/octoport-app"
        fi
      fi
      ;;
    macos)
      ASSET="octoport-app-${OS}-${ARCH}.dmg"
      if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
        download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
        install_dmg "$ASSET"
      else
        ASSET="octoport-app-${OS}-${ARCH}.pkg"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
          install_pkg "$ASSET"
        else
          ASSET="octoport-app-${OS}-${ARCH}"
          verify_and_install_binary "$ASSET" "/Applications/OctoPort.app/Contents/MacOS/octoport-app"
        fi
      fi
      ;;
    windows)
      ASSET="octoport-app-${OS}-${ARCH}.msi"
      if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
        download_asset "${BASE_URL}/${ASSET}" "$TMP/${ASSET}"
        install_msi "$ASSET"
      else
        ASSET="octoport-app-${OS}-${ARCH}.exe"
        verify_and_install_binary "$ASSET" "$DEST_DIR/octoport-app.exe"
      fi
      ;;
  esac
fi

echo
spinner_stop
echo "Installation complete."
if [ "$INSTALL_CLI" = "true" ]; then
  echo "  CLI: octoport login && octoport expose 3000"
fi
if [ "$INSTALL_GUI" = "true" ]; then
  case "$OS" in
    linux) echo "  GUI: octoport-app (or run from app menu)" ;;
    macos) echo "  GUI: Open OctoPort from Applications" ;;
    windows) echo "  GUI: octoport-app (or run from Start menu)" ;;
  esac
fi