// Package commsctl is the core-side controller for the shipyard-comms
// addon. It handles binary resolution, installation, uninstallation and
// upgrade, mirroring the shape of internal/fairwayctl and internal/crewctl
// so the rest of the shipyard CLI sees one consistent contract per addon.
//
// Comms is stateless in v1: there is no daemon, no socket, no service
// registration. Consequently commsctl exposes only the install/uninstall
// lifecycle, not a runtime client.
package commsctl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Platform identifies the target OS and CPU architecture for a release
// artifact. Matches the shape used in fairwayctl/crewctl so callers can
// pass through values from the same source (runtime.GOOS/GOARCH).
type Platform struct {
	OS   string
	Arch string
}

// CurrentPlatform returns the Platform for the running process.
func CurrentPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// DefaultReleaseBase is the GitHub releases base URL.
const DefaultReleaseBase = "https://github.com/shipyard-auto/shipyard/releases/download"

// BinaryName is the filename of the installed comms binary.
const BinaryName = "shipyard-comms"

// ErrNotInstalled is returned by ResolveBinary when the comms binary
// cannot be located on disk.
var ErrNotInstalled = errors.New("comms: binary not installed")

// ResolveBinary returns the absolute path to an installed shipyard-comms
// binary, or ErrNotInstalled when no usable binary is found. It looks
// first at the default install prefix (~/.local/bin), then falls back to
// PATH. Mirrors fairwayctl.ResolveBinary so the behavior is uniform
// across addons.
func ResolveBinary() (string, error) {
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".local", "bin", BinaryName)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath(BinaryName); err == nil {
		return path, nil
	}
	return "", ErrNotInstalled
}

// ReleaseTag returns the GitHub release tag used by comms artifacts.
func ReleaseTag(version string) string {
	return fmt.Sprintf("comms-v%s", version)
}

// ArtifactName returns the release archive name for a given version and
// platform.
func ArtifactName(version string, p Platform) string {
	return fmt.Sprintf("shipyard-comms_%s_%s_%s.tar.gz", version, p.OS, p.Arch)
}

// ChecksumManifestName returns the checksum manifest file name for a
// comms release.
func ChecksumManifestName(version string) string {
	return fmt.Sprintf("shipyard-comms_%s_checksums.txt", version)
}
