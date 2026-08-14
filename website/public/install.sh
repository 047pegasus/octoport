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

# Version path
if [ "$VERSION" = "latest" ]; then
  BASE_URL="$REPO/releases/latest/download"
  SHA256SUMS_URL="$REPO/releases/latest/download/SHA256SUMS"
else
  BASE_URL="$REPO/releases/download/$VERSION"
  SHA256SUMS_URL="$REPO/releases/download/$VERSION/SHA256SUMS"
fi

# Uninstall logic
if [ "$UNINSTALL" = "true" ]; then
  echo "Uninstalling OctoPort..."

  # CLI binary
  if [ "$INSTALL_CLI" = "true" ]; then
    echo "Removing CLI..."
    sudo rm -f /usr/local/bin/octoport
    # Debian/Ubuntu
    sudo apt-get remove --purge octoport 2>/dev/null || true
    # RHEL/Fedora
    sudo rpm -e octoport 2>/dev/null || true
    # macOS
    sudo pkgutil --forget io.octoport.cli 2>/dev/null || true
    sudo rm -rf /usr/local/bin/octoport /Applications/OctoPort.app 2>/dev/null || true
    echo "  CLI removed"
  fi

  # GUI
  if [ "$INSTALL_GUI" = "true" ]; then
    echo "Removing GUI..."
    # Linux
    sudo apt-get remove --purge octoport-app 2>/dev/null || true
    sudo rpm -e octoport-app 2>/dev/null || true
    # macOS
    sudo pkgutil --forget io.octoport.gui 2>/dev/null || true
    sudo rm -rf /Applications/OctoPort.app /opt/octoport-app.AppImage /usr/local/bin/octoport-app 2>/dev/null || true
    # Windows (via winget/scoop if available, but we just clean up)
    echo "  GUI removed"
  fi

  # Config directories
  rm -rf ~/.config/octoport ~/.config/octoport-app
  rm -rf ~/.local/share/octoport ~/.local/share/octoport-app

  echo "OctoPort uninstalled."
  exit 0
fi

echo "Installing OctoPort ${VERSION} ($OS/$ARCH) from $REPO"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Fetch SHA256SUMS once
echo "Fetching checksums..."
if ! curl -fsSL "$SHA256SUMS_URL" -o "$TMP/SHA256SUMS" 2>/dev/null; then
  echo "! could not fetch SHA256SUMS; aborting" >&2
  exit 1
fi

# Verify checksum function
verify_and_install_binary() {
  local asset_name="$1"
  local dest_path="$2"
  local expected_checksum

  expected_checksum=$(grep " ${asset_name}\$" "$TMP/SHA256SUMS" | awk '{print $1}')
  if [ -z "$expected_checksum" ]; then
    echo "! no checksum published for ${asset_name}; aborting" >&2
    exit 1
  fi

  local url="${BASE_URL}/${asset_name}"
  echo "Downloading ${asset_name}..."
  curl -fsSL "$url" -o "$TMP/${asset_name}"

  # Verify checksum
  local actual_checksum
  if command -v sha256sum >/dev/null 2>&1; then
    actual_checksum=$(sha256sum "$TMP/${asset_name}" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual_checksum=$(shasum -a 256 "$TMP/${asset_name}" | awk '{print $1}')
  else
    echo "! neither sha256sum nor shasum found; cannot verify download" >&2
    exit 1
  fi

  if [ "$expected_checksum" != "$actual_checksum" ]; then
    echo "! checksum mismatch for ${asset_name} - refusing to install" >&2
    echo "  expected $expected_checksum" >&2
    echo "  actual   $actual_checksum" >&2
    exit 1
  fi
  echo "  checksum ok"

  # Install binary
  if [ ! -w "$(dirname "$dest_path")" ]; then
    echo "! $dest_path requires elevated privileges; using sudo"
    sudo install -m 755 "$TMP/${asset_name}" "$dest_path"
  else
    install -m 755 "$TMP/${asset_name}" "$dest_path"
  fi
}

install_deb() {
  local asset="$1"
  echo "Installing .deb package: $asset"
  sudo apt-get update -qq && sudo apt-get install -y "$TMP/$asset"
}

install_rpm() {
  local asset="$1"
  echo "Installing .rpm package: $asset"
  sudo dnf install -y "$TMP/$asset" 2>/dev/null || sudo yum install -y "$TMP/$asset"
}

install_dmg() {
  local asset="$1"
  echo "Mounting and installing .dmg: $asset"
  local mount_point=$(hdiutil attach "$TMP/$asset" -nobrowse -quiet | tail -1 | awk '{print $3}')
  if [ -z "$mount_point" ]; then
    echo "! failed to mount DMG" >&2
    exit 1
  fi
  # Find .app or .pkg inside
  if find "$mount_point" -name "*.app" -maxdepth 1 | grep -q .; then
    local app_path=$(find "$mount_point" -name "*.app" -maxdepth 1 | head -1)
    echo "Installing .app to /Applications..."
    sudo cp -R "$app_path" /Applications/
  elif find "$mount_point" -name "*.pkg" -maxdepth 1 | grep -q .; then
    local pkg_path=$(find "$mount_point" -name "*.pkg" -maxdepth 1 | head -1)
    echo "Installing .pkg..."
    sudo installer -pkg "$pkg_path" -target /
  fi
  hdiutil detach "$mount_point" -quiet
}

install_pkg() {
  local asset="$1"
  echo "Installing .pkg: $asset"
  sudo installer -pkg "$TMP/$asset" -target /
}

install_msi() {
  local asset="$1"
  echo "Installing .msi: $asset"
  msiexec /i "$TMP/$asset" /quiet /norestart
}

install_appimage() {
  local asset="$1"
  local dest="/opt/octoport-app.AppImage"
  echo "Installing AppImage to $dest..."
  if [ ! -w "/opt" ]; then
    sudo install -m 755 "$TMP/$asset" "$dest"
    sudo ln -sf "$dest" /usr/local/bin/octoport-app
  else
    install -m 755 "$TMP/$asset" "$dest"
    ln -sf "$dest" /usr/local/bin/octoport-app
  fi
}

# Main install logic
if [ "$INSTALL_CLI" = "true" ]; then
  echo "=== Installing CLI ==="
  case "$OS" in
    linux)
      # Prefer .deb/.rpm on supported distros, fallback to tar.gz
      if command -v dpkg >/dev/null 2>&1; then
        ASSET="octoport-${OS}-${ARCH}.deb"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
          install_deb "$ASSET"
        else
          ASSET="octoport-${OS}-${ARCH}.tar.gz"
          curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
          tar xzf "$TMP/${ASSET}" -C "$TMP"
          verify_and_install_binary "octoport-${OS}-${ARCH}" "/usr/local/bin/octoport"
        fi
      elif command -v rpm >/dev/null 2>&1 || command -v dnf >/dev/null 2>&1; then
        ASSET="octoport-${OS}-${ARCH}.rpm"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
          install_rpm "$ASSET"
        else
          ASSET="octoport-${OS}-${ARCH}.tar.gz"
          curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
          tar xzf "$TMP/${ASSET}" -C "$TMP"
          verify_and_install_binary "octoport-${OS}-${ARCH}" "/usr/local/bin/octoport"
        fi
      else
        ASSET="octoport-${OS}-${ARCH}.tar.gz"
        curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
        tar xzf "$TMP/${ASSET}" -C "$TMP"
        verify_and_install_binary "octoport-${OS}-${ARCH}" "$DEST_DIR/octoport"
      fi
      ;;
    macos)
      ASSET="octoport-${OS}-${ARCH}.pkg"
      if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
        curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
        install_pkg "$ASSET"
      else
        ASSET="octoport-${OS}-${ARCH}"
        verify_and_install_binary "$ASSET" "$DEST_DIR/octoport"
      fi
      ;;
    windows)
      ASSET="octoport-${OS}-${ARCH}.msi"
      if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
        curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
        install_msi "$ASSET"
      else
        ASSET="octoport-${OS}-${ARCH}.exe"
        verify_and_install_binary "$ASSET" "$DEST_DIR/octoport.exe"
      fi
      ;;
  esac
fi

if [ "$INSTALL_GUI" = "true" ]; then
  echo "=== Installing GUI ==="
  case "$OS" in
    linux)
      # Prefer AppImage or deb/rpm
      if command -v dpkg >/dev/null 2>&1; then
        ASSET="octoport-app-${OS}-${ARCH}.deb"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
          install_deb "$ASSET"
        else
          ASSET="octoport-app-${OS}-${ARCH}.AppImage"
          if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
            curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
            install_appimage "$ASSET"
          fi
        fi
      elif command -v rpm >/dev/null 2>&1 || command -v dnf >/dev/null 2>&1; then
        ASSET="octoport-app-${OS}-${ARCH}.rpm"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
          install_rpm "$ASSET"
        else
          ASSET="octoport-app-${OS}-${ARCH}.AppImage"
          if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
            curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
            install_appimage "$ASSET"
          fi
        fi
      else
        ASSET="octoport-app-${OS}-${ARCH}.AppImage"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
          install_appimage "$ASSET"
        else
          ASSET="octoport-app-${OS}-${ARCH}.tar.gz"
          curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
          tar xzf "$TMP/${ASSET}" -C "$TMP"
          verify_and_install_binary "octoport-app-${OS}-${ARCH}" "/usr/local/bin/octoport-app"
        fi
      fi
      ;;
    macos)
      ASSET="octoport-app-${OS}-${ARCH}.dmg"
      if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
        curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
        install_dmg "$ASSET"
      else
        ASSET="octoport-app-${OS}-${ARCH}.pkg"
        if grep -q " ${ASSET}\$" "$TMP/SHA256SUMS"; then
          curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
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
        curl -fsSL "${BASE_URL}/${ASSET}" -o "$TMP/${ASSET}"
        install_msi "$ASSET"
      else
        ASSET="octoport-app-${OS}-${ARCH}.exe"
        verify_and_install_binary "$ASSET" "$DEST_DIR/octoport-app.exe"
      fi
      ;;
  esac
fi

echo
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