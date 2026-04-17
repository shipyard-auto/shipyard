#!/usr/bin/env sh

set -eu

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

detect_platform() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$os" in
    linux|darwin) ;;
    *) fail "unsupported operating system: $os" ;;
  esac

  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) fail "unsupported architecture: $arch" ;;
  esac

  PLATFORM_OS="$os"
  PLATFORM_ARCH="$arch"
}

main() {
  need_cmd gh
  need_cmd tar
  need_cmd shasum
  need_cmd mktemp

  tag="${1:-}"
  [ -n "$tag" ] || fail "usage: validate_release.sh fairway-v<version>"

  case "$tag" in
    fairway-v*) ;;
    *) fail "invalid tag: $tag" ;;
  esac

  version="${tag#fairway-v}"
  repo="${GITHUB_REPOSITORY:-shipyard-auto/shipyard}"
  binary_name="shipyard-fairway"
  manifest_name="${binary_name}_${version}_checksums.txt"

  detect_platform

  artifact_name="${binary_name}_${version}_${PLATFORM_OS}_${PLATFORM_ARCH}.tar.gz"

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT INT TERM

  gh release download "$tag" \
    --repo "$repo" \
    --dir "$tmpdir" \
    --pattern "$artifact_name" \
    --pattern "$manifest_name"

  [ -f "$tmpdir/$artifact_name" ] || fail "artifact not downloaded: $artifact_name"
  [ -f "$tmpdir/$manifest_name" ] || fail "checksum manifest not downloaded: $manifest_name"

  (
    cd "$tmpdir"
    grep "  ${artifact_name}$" "$manifest_name" | shasum -a 256 -c >/dev/null
  ) || fail "checksum validation failed for $artifact_name"

  tar -xzf "$tmpdir/$artifact_name" -C "$tmpdir"
  [ -x "$tmpdir/$binary_name" ] || fail "binary not extracted: $binary_name"

  version_out="$("$tmpdir/$binary_name" --version)"
  printf '%s' "$version_out" | grep -F "$version" >/dev/null || fail "binary version output does not contain $version: $version_out"

  printf 'ok: validated %s from %s on %s/%s\n' "$artifact_name" "$tag" "$PLATFORM_OS" "$PLATFORM_ARCH"
}

main "$@"
