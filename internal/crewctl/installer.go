package crewctl

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultReleaseBase is the GitHub releases base URL used for crew artifacts.
const DefaultReleaseBase = "https://github.com/shipyard-auto/shipyard/releases/download"

// BinaryName is the filename of the installed crew binary.
const BinaryName = "shipyard-crew"

// maxDownloadBytes caps the size of a downloaded artifact to avoid runaway
// writes when the server returns an unexpected body.
const maxDownloadBytes = 64 * 1024 * 1024

// ErrNotInstalled is returned by InstalledVersion when the binary is absent.
var ErrNotInstalled = errors.New("crew: binary not installed")

// ErrAlreadyInstalled is returned by Install when the binary is already at the
// requested version and Force is false.
var ErrAlreadyInstalled = errors.New("crew: already installed at current version")

// ErrUpgradeRequired is returned by Install when a different version is
// already installed and Force is false.
var ErrUpgradeRequired = errors.New("crew: different version installed — pass --force to reinstall")

// ErrPlatformUnsupported is returned when the platform pair is not recognised.
var ErrPlatformUnsupported = errors.New("crew: unsupported platform")

// HTTPClient is the minimal HTTP contract used by the Installer; injected for
// deterministic tests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Platform identifies the target OS and CPU architecture for a release asset.
type Platform struct {
	OS   string
	Arch string
}

// CurrentPlatform returns the platform for the running process.
func CurrentPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// SupportedPlatforms enumerates the (os, arch) pairs released for shipyard-crew.
var SupportedPlatforms = []Platform{
	{OS: "linux", Arch: "amd64"},
	{OS: "linux", Arch: "arm64"},
	{OS: "darwin", Arch: "amd64"},
	{OS: "darwin", Arch: "arm64"},
}

// Validate returns an error if the platform pair is not among the supported
// release matrix.
func (p Platform) Validate() error {
	for _, sp := range SupportedPlatforms {
		if sp == p {
			return nil
		}
	}
	return fmt.Errorf("%w: %s/%s", ErrPlatformUnsupported, p.OS, p.Arch)
}

// ReleaseTag returns the GitHub release tag used for crew artifacts at version.
func ReleaseTag(version string) string {
	return fmt.Sprintf("crew-v%s", version)
}

// ArtifactName returns the release archive name for a given version and platform.
func ArtifactName(version string, p Platform) string {
	return fmt.Sprintf("shipyard-crew_%s_%s_%s.tar.gz", version, p.OS, p.Arch)
}

// ChecksumManifestName returns the checksum manifest file name for a release.
func ChecksumManifestName(version string) string {
	return fmt.Sprintf("shipyard-crew_%s_checksums.txt", version)
}

// Installer handles download, verification and atomic install of the
// shipyard-crew binary. Unlike fairway, the installer does NOT register any
// OS service — per-agent services are registered by `shipyard crew hire`.
type Installer struct {
	Version     string
	Platform    Platform
	BinDir      string
	StateDir    string // ~/.shipyard/crew — preserved on uninstall
	RunDir      string // ~/.shipyard/run/crew — socket and pidfile location
	Force       bool
	HTTPClient  HTTPClient
	ReleaseBase string
	Warn        io.Writer // PATH warning destination; defaults to os.Stderr
	Now         func() time.Time

	// rename is os.Rename by default; overridable in tests.
	rename func(old, new string) error
}

// BinPath returns the absolute path of the installed crew binary.
func (i *Installer) BinPath() string {
	return filepath.Join(i.BinDir, BinaryName)
}

func (i *Installer) renameFn() func(string, string) error {
	if i.rename != nil {
		return i.rename
	}
	return os.Rename
}

func (i *Installer) warnWriter() io.Writer {
	if i.Warn != nil {
		return i.Warn
	}
	return os.Stderr
}

// ResolveBinary returns the absolute path to an installed shipyard-crew
// binary, or ErrNotInstalled when no usable binary is found. It looks first
// at the default install prefix (~/.local/bin), then falls back to PATH.
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

// InstalledVersion runs the installed binary with --version and returns the
// semver token. Returns ErrNotInstalled when the binary is absent.
func (i *Installer) InstalledVersion() (string, error) {
	binPath := i.BinPath()
	info, err := os.Stat(binPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotInstalled
	}
	if err != nil {
		return "", fmt.Errorf("crew: stat binary %s: %w", binPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("crew: %s is a directory", binPath)
	}

	out, err := exec.Command(binPath, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("crew: exec --version: %w", err)
	}
	return parseVersionOutput(string(out)), nil
}

// parseVersionOutput extracts the semver token from `shipyard-crew --version`
// output, which has the form: "shipyard-crew <version> (<hash>, built <ts>)".
// Falls back to the trimmed input when the prefix is missing.
func parseVersionOutput(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "shipyard-crew ") {
		parts := strings.Fields(trimmed)
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return trimmed
}

// Install downloads and installs shipyard-crew at i.Version for i.Platform.
//
// Flow:
//  1. Validate the platform.
//  2. Unless Force: short-circuit on same version / error on different version.
//  3. Download artifact and checksums.
//  4. Verify SHA-256.
//  5. Extract into a temp file in BinDir, chmod 0755, atomic rename into place.
//  6. Verify the installed binary reports the expected version.
//  7. Emit a PATH warning if BinDir is not in $PATH.
func (i *Installer) Install(ctx context.Context) error {
	if err := i.Platform.Validate(); err != nil {
		return err
	}

	if !i.Force {
		installed, err := i.InstalledVersion()
		if err == nil {
			if installed == i.Version {
				return ErrAlreadyInstalled
			}
			return fmt.Errorf("%w (installed: %s, want: %s)", ErrUpgradeRequired, installed, i.Version)
		}
		if !errors.Is(err, ErrNotInstalled) {
			// A non-ENOENT error (e.g. exec failure) should not mask the
			// caller's intent to install; proceed.
		}
	}

	artifact := ArtifactName(i.Version, i.Platform)
	tag := ReleaseTag(i.Version)
	artifactURL := fmt.Sprintf("%s/%s/%s", i.ReleaseBase, tag, artifact)
	checksumName := ChecksumManifestName(i.Version)
	checksumURL := fmt.Sprintf("%s/%s/%s", i.ReleaseBase, tag, checksumName)

	tmpArtifact, err := i.download(ctx, artifactURL)
	if err != nil {
		return fmt.Errorf("crew: failed to download %s: %w", artifact, err)
	}
	defer os.Remove(tmpArtifact)

	tmpChecksums, err := i.download(ctx, checksumURL)
	if err != nil {
		return fmt.Errorf("crew: failed to download %s: %w", checksumName, err)
	}
	defer os.Remove(tmpChecksums)

	expectedSHA, err := extractSHA(tmpChecksums, artifact)
	if err != nil {
		return fmt.Errorf("crew: checksum lookup in %s: %w", checksumName, err)
	}

	if err := verifyChecksum(tmpArtifact, expectedSHA); err != nil {
		return fmt.Errorf("crew: checksum: %w", err)
	}

	if err := os.MkdirAll(i.BinDir, 0o755); err != nil {
		return fmt.Errorf("crew: create bin dir: %w", err)
	}
	if i.StateDir != "" {
		if err := os.MkdirAll(i.StateDir, 0o700); err != nil {
			return fmt.Errorf("crew: create state dir: %w", err)
		}
	}
	if i.RunDir != "" {
		if err := os.MkdirAll(i.RunDir, 0o700); err != nil {
			return fmt.Errorf("crew: create run dir: %w", err)
		}
	}

	tmpBin, err := os.CreateTemp(i.BinDir, ".shipyard-crew-*")
	if err != nil {
		return fmt.Errorf("crew: create temp binary: %w", err)
	}
	tmpBinPath := tmpBin.Name()
	tmpBin.Close()
	defer os.Remove(tmpBinPath)

	if err := extractBinary(tmpArtifact, tmpBinPath); err != nil {
		return fmt.Errorf("crew: extract: %w", err)
	}

	if err := os.Chmod(tmpBinPath, 0o755); err != nil {
		return fmt.Errorf("crew: chmod: %w", err)
	}

	dest := i.BinPath()
	if err := i.renameFn()(tmpBinPath, dest); err != nil {
		return fmt.Errorf("crew: install binary: %w", err)
	}

	installed, err := i.InstalledVersion()
	if err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("crew: verify installed version: %w", err)
	}
	if installed != i.Version {
		_ = os.Remove(dest)
		return fmt.Errorf("crew: installed version %q does not match requested %q", installed, i.Version)
	}

	i.maybeWarnPATH()
	return nil
}

// maybeWarnPATH writes a notice to the Warn writer when BinDir is not present
// in the PATH environment variable. It never fails the install.
func (i *Installer) maybeWarnPATH() {
	path := os.Getenv("PATH")
	target := filepath.Clean(i.BinDir)
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		if filepath.Clean(dir) == target {
			return
		}
	}
	fmt.Fprintf(i.warnWriter(), "warning: %s is not in your PATH; add it so you can run shipyard-crew directly\n", i.BinDir)
}

// Uninstall removes the binary from BinDir. StateDir and RunDir are preserved
// on purpose — removing agent configuration is a separate, explicit action.
// Uninstall does NOT deregister per-agent services; callers must run
// `shipyard crew fire <name>` first.
func (i *Installer) Uninstall(ctx context.Context) error {
	if err := os.Remove(i.BinPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("crew: remove binary: %w", err)
	}
	return nil
}

// download fetches url via HTTPClient and streams the body to a temp file.
// The caller is responsible for removing the returned path.
func (i *Installer) download(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "shipyard-crew/"+i.Version)

	resp, err := i.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("HTTP %d for %s: %s", resp.StatusCode, url, strings.TrimSpace(string(body)))
	}

	tmp, err := os.CreateTemp("", "shipyard-crew-dl-*")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if n > maxDownloadBytes {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("download exceeds %d bytes", maxDownloadBytes)
	}

	return tmp.Name(), nil
}

// verifyChecksum computes SHA-256 of path and compares it to expected.
func verifyChecksum(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, expected)
	}
	return nil
}

// extractBinary extracts the file named BinaryName from a .tar.gz archive and
// writes it to dest.
func extractBinary(tarGzPath, dest string) error {
	f, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) != BinaryName {
			continue
		}

		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			os.Remove(dest)
			return err
		}
		return out.Close()
	}
	return fmt.Errorf("%s binary not found in archive", BinaryName)
}

// extractSHA finds the SHA-256 hex string for artifactName in a checksum
// manifest file.
func extractSHA(checksumPath, artifactName string) (string, error) {
	f, err := os.Open(checksumPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) >= 2 && parts[1] == artifactName {
			return parts[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no checksum found for %q", artifactName)
}

// DefaultHTTPClient returns an http.Client with a conservative timeout for
// production callers.
func DefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}
