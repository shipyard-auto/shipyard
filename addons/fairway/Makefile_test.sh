#!/usr/bin/env sh

set -eu

log() {
  printf '%s\n' "$1"
}

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

root_dir() {
  CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd
}

count_tarballs() {
  find "$1" -maxdepth 1 -type f -name '*.tar.gz' | wc -l | tr -d ' '
}

assert_file() {
  [ -f "$1" ] || fail "expected file not found: $1"
}

assert_exec_0755() {
  [ -x "$1" ] || fail "expected executable file: $1"
  mode="$(stat --format='%a' "$1" 2>/dev/null || stat -f '%Lp' "$1")"
  [ "$mode" = "755" ] || fail "expected mode 755 for $1, got $mode"
}

assert_tar_single_binary() {
  archive="$1"
  expected="$2"
  listing="$(tar -tzf "$archive")"
  [ "$listing" = "$expected" ] || fail "archive $archive should contain exactly $expected, got: $listing"
}

assert_checksum_manifest() {
  dist_dir="$1"
  manifest="$2"
  (
    cd "$dist_dir"
    shasum -a 256 -c "$manifest" >/dev/null
  ) || fail "checksum validation failed for $manifest"
}

main() {
  need_cmd make
  need_cmd go
  need_cmd tar
  need_cmd shasum
  need_cmd mktemp

  repo_root="$(root_dir)"
  version="$(cat "$repo_root/VERSION")"
  binary_name="shipyard-fairway"
  manifest_name="${binary_name}_${version}_checksums.txt"

  tmpdir="$(mktemp -d)"
  dist_dir="$tmpdir/dist"
  trap 'rm -rf "$tmpdir"' EXIT INT TERM

  cd "$repo_root"

  log "scenario: make_build_fairway_producesBinary"
  make DIST_DIR="$dist_dir" clean >/dev/null
  make DIST_DIR="$dist_dir" build-fairway >/dev/null
  assert_file "$dist_dir/$binary_name"
  assert_exec_0755 "$dist_dir/$binary_name"
  version_out="$("$dist_dir/$binary_name" --version)"
  printf '%s' "$version_out" | grep -F "$version" >/dev/null || fail "binary version output does not contain $version: $version_out"

  log "scenario: make_dist_fairway_producesFourPlatforms"
  make DIST_DIR="$dist_dir" dist-fairway >/dev/null
  for platform in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
    bin_path="$dist_dir/fairway-$platform/$binary_name"
    assert_file "$bin_path"
    [ -s "$bin_path" ] || fail "expected non-empty binary: $bin_path"
  done

  log "scenario: make_package_fairway_producesValidTarballs"
  make DIST_DIR="$dist_dir" package-fairway >/dev/null
  [ "$(count_tarballs "$dist_dir")" -eq 4 ] || fail "expected 4 fairway tarballs after package-fairway"
  for archive in \
    "$dist_dir/${binary_name}_${version}_linux_amd64.tar.gz" \
    "$dist_dir/${binary_name}_${version}_linux_arm64.tar.gz" \
    "$dist_dir/${binary_name}_${version}_darwin_amd64.tar.gz" \
    "$dist_dir/${binary_name}_${version}_darwin_arm64.tar.gz"
  do
    assert_file "$archive"
    assert_tar_single_binary "$archive" "$binary_name"
  done

  log "scenario: make_checksums_fairway_producesValidManifest"
  make DIST_DIR="$dist_dir" checksums-fairway >/dev/null
  assert_file "$dist_dir/$manifest_name"
  assert_checksum_manifest "$dist_dir" "$manifest_name"

  log "scenario: make_package_all_includesCoreAndFairway"
  make DIST_DIR="$dist_dir" package-all >/dev/null
  [ "$(count_tarballs "$dist_dir")" -eq 8 ] || fail "expected 8 total tarballs after package-all"

  log "scenario: build_does_not_regress"
  make DIST_DIR="$dist_dir" build >/dev/null
  assert_file "$dist_dir/shipyard"

  log "scenario: make_clean_removesAllArtifacts"
  make DIST_DIR="$dist_dir" clean >/dev/null
  [ ! -e "$dist_dir" ] || fail "dist directory should not exist after make clean"

  log "running go test ./addons/fairway/... after build-fairway contract"
  make DIST_DIR="$dist_dir" build-fairway >/dev/null
  go test ./addons/fairway/... >/dev/null

  log "ok: all fairway makefile checks passed"
}

main "$@"
