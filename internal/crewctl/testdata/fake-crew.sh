#!/bin/sh
# Fixture used by installer_test.go as the "binary" packaged into the test
# tarball. When invoked with --version it prints the expected version line
# consumed by Installer.InstalledVersion.
case "$1" in
  --version)
    echo "shipyard-crew ${SHIPYARD_CREW_VERSION:-0.1.0} (test, built 2026-04-20)"
    ;;
  *)
    echo "fake shipyard-crew" ;;
esac
