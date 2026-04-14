#!/usr/bin/env sh

set -eu

OWNER="${OWNER:-shipyard-auto}"
REPO="${REPO:-shipyard}"
VERSION="${VERSION:-}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
SHIPYARD_HOME="${SHIPYARD_HOME:-$HOME/.shipyard}"

if [ -t 1 ] && [ "${NO_COLOR:-}" = "" ]; then
  C_RESET="$(printf '\033[0m')"
  C_BOLD="$(printf '\033[1m')"
  C_DIM="$(printf '\033[2m')"
  C_CYAN="$(printf '\033[36m')"
  C_BLUE="$(printf '\033[34m')"
  C_GREEN="$(printf '\033[32m')"
  C_YELLOW="$(printf '\033[33m')"
  C_RED="$(printf '\033[31m')"
else
  C_RESET=""
  C_BOLD=""
  C_DIM=""
  C_CYAN=""
  C_BLUE=""
  C_GREEN=""
  C_YELLOW=""
  C_RED=""
fi

say() {
  printf '%s\n' "$1"
}

info() {
  printf '%s==>%s %s\n' "$C_CYAN" "$C_RESET" "$1"
}

success() {
  printf '%s==>%s %s\n' "$C_GREEN" "$C_RESET" "$1"
}

warn() {
  printf '%s==>%s %s\n' "$C_YELLOW" "$C_RESET" "$1"
}

fail() {
  printf '%s==>%s %s\n' "$C_RED" "$C_RESET" "$1" >&2
  exit 1
}

banner() {
  say ""
  say "${C_BLUE}${C_BOLD}                               |    |    |                              ${C_RESET}"
  say "${C_BLUE}${C_BOLD}                              )_)  )_)  )_)                             ${C_RESET}"
  say "${C_BLUE}${C_BOLD}                             )___))___))___)\\                           ${C_RESET}"
  say "${C_BLUE}${C_BOLD}                            )____)____)_____)\\\\                         ${C_RESET}"
  say "${C_BLUE}${C_BOLD}                          _____|____|____|____\\\\__                      ${C_RESET}"
  say "${C_BLUE}${C_BOLD}                 ---------\\                       /---------             ${C_RESET}"
  say "${C_CYAN}                   ^^^^^ ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^               ${C_RESET}"
  say "${C_BOLD}                          SHIPYARD :: INSTALLER                         ${C_RESET}"
  say "${C_DIM}                    Build, install and service your fleet              ${C_RESET}"
  say ""
}

panel() {
  say "${C_CYAN}${C_BOLD}Configuration${C_RESET}"
  say "  ${C_DIM}Release:${C_RESET} $VERSION"
  say "  ${C_DIM}Target:${C_RESET}  $PLATFORM_OS/$PLATFORM_ARCH"
  say "  ${C_DIM}Binary:${C_RESET}  $INSTALL_DIR/shipyard"
  say "  ${C_DIM}Home:${C_RESET}    $SHIPYARD_HOME"
  say ""
}

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "required command not found: $1"
  fi
}

resolve_version() {
  if [ -n "$VERSION" ]; then
    return
  fi

  api_url="https://api.github.com/repos/$OWNER/$REPO/releases/latest"
  VERSION="$(curl -fsSL "$api_url" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"

  if [ -z "$VERSION" ]; then
    fail "unable to resolve the latest release version"
  fi
}

detect_platform() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$os" in
    linux|darwin) ;;
    *)
      fail "unsupported operating system: $os"
      ;;
  esac

  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *)
      fail "unsupported architecture: $arch"
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

run_step() {
  message="$1"
  shift

  info "$message"
  if "$@"; then
    success "$message"
    return 0
  fi

  fail "$message failed"
}

write_manifest() {
  cat > "$SHIPYARD_HOME/install.json" <<EOF
{
  "version": "${VERSION#v}",
  "binary_path": "$INSTALL_DIR/shipyard",
  "home_dir": "$SHIPYARD_HOME",
  "installed_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
}
EOF
}

main() {
  need_cmd curl
  need_cmd tar
  need_cmd mktemp
  need_cmd install

  banner

  resolve_version
  detect_platform
  panel

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT INT TERM

  archive_path="$tmpdir/shipyard.tar.gz"
  url="$(download_url)"

  run_step "Downloading release package" curl -fsSL "$url" -o "$archive_path"

  mkdir -p "$INSTALL_DIR"
  run_step "Extracting package" tar -xzf "$archive_path" -C "$tmpdir"

  mkdir -p "$SHIPYARD_HOME"
  run_step "Installing shipyard binary" install -m 0755 "$tmpdir/shipyard" "$INSTALL_DIR/shipyard"

  write_manifest
  success "Wrote install metadata to $SHIPYARD_HOME/install.json"

  if [ -x "$INSTALL_DIR/shipyard" ]; then
    "$INSTALL_DIR/shipyard" --help >/dev/null 2>&1 || warn "Installed binary did not pass help validation"
  fi

  case ":$PATH:" in
    *":$INSTALL_DIR:"*) path_ok=1 ;;
    *) path_ok=0 ;;
  esac

  say ""
  say "${C_GREEN}${C_BOLD}Shipyard installed successfully.${C_RESET}"
  say "${C_DIM}Your yard is ready.${C_RESET}"
  say ""
  say "${C_BOLD}Next steps${C_RESET}"
  say "  ${C_CYAN}1.${C_RESET} ${C_BOLD}$INSTALL_DIR/shipyard help${C_RESET}"
  say "  ${C_CYAN}2.${C_RESET} ${C_BOLD}$INSTALL_DIR/shipyard version${C_RESET}"

  if [ "$path_ok" -eq 0 ]; then
    say ""
    warn "$INSTALL_DIR is not in PATH. Use the full binary path or add it to your shell profile."
  fi
}

main "$@"
