#!/usr/bin/env sh

set -eu

OWNER="${OWNER:-shipyard-auto}"
REPO="${REPO:-shipyard}"
VERSION="${VERSION:-}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
SHIPYARD_HOME="${SHIPYARD_HOME:-$HOME/.shipyard}"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'shipyard installer error: required command not found: %s\n' "$1" >&2
    exit 1
  fi
}

resolve_version() {
  if [ -n "$VERSION" ]; then
    return
  fi

  api_url="https://api.github.com/repos/$OWNER/$REPO/releases/latest"
  VERSION="$(curl -fsSL "$api_url" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"

  if [ -z "$VERSION" ]; then
    printf 'shipyard installer error: unable to resolve the latest release version\n' >&2
    exit 1
  fi
}

detect_platform() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$os" in
    linux|darwin) ;;
    *)
      printf 'shipyard installer error: unsupported operating system: %s\n' "$os" >&2
      exit 1
      ;;
  esac

  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *)
      printf 'shipyard installer error: unsupported architecture: %s\n' "$arch" >&2
      exit 1
      ;;
  esac

  PLATFORM_OS="$os"
  PLATFORM_ARCH="$arch"
}

download_url() {
  version_no_v="${VERSION#v}"
  archive="shipyard_${version_no_v}_${PLATFORM_OS}_${PLATFORM_ARCH}.tar.gz"
  printf 'https://github.com/%s/%s/releases/download/%s/%s' "$OWNER" "$REPO" "$VERSION" "$archive"
}

main() {
  need_cmd curl
  need_cmd tar
  need_cmd mktemp

  resolve_version
  detect_platform

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT INT TERM

  archive_path="$tmpdir/shipyard.tar.gz"
  url="$(download_url)"

  printf 'Installing Shipyard %s for %s/%s\n' "$VERSION" "$PLATFORM_OS" "$PLATFORM_ARCH"
  printf 'Downloading %s\n' "$url"

  curl -fsSL "$url" -o "$archive_path"

  mkdir -p "$INSTALL_DIR"
  tar -xzf "$archive_path" -C "$tmpdir"

  install -m 0755 "$tmpdir/shipyard" "$INSTALL_DIR/shipyard"
  mkdir -p "$SHIPYARD_HOME"
  cat > "$SHIPYARD_HOME/install.json" <<EOF
{
  "version": "${VERSION#v}",
  "binary_path": "$INSTALL_DIR/shipyard",
  "home_dir": "$SHIPYARD_HOME",
  "installed_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
}
EOF

  printf 'Installed binary to %s/shipyard\n' "$INSTALL_DIR"
  printf 'Created Shipyard home at %s\n' "$SHIPYARD_HOME"

  if [ -x "$INSTALL_DIR/shipyard" ]; then
    "$INSTALL_DIR/shipyard" --help >/dev/null
  fi

  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
      printf 'Warning: %s is not in PATH. Add it to use shipyard directly.\n' "$INSTALL_DIR"
      ;;
  esac

  printf 'Run: %s/shipyard help\n' "$INSTALL_DIR"
}

main "$@"
