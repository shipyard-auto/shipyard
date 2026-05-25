package commsctl

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// commsReleasesAPI is the GitHub releases listing endpoint used to discover
// the latest published comms release.
const commsReleasesAPI = "https://api.github.com/repos/shipyard-auto/shipyard/releases?per_page=30"

// installedVersionTimeout caps how long the installer waits for the
// installed binary to respond to `--version`. Mirrors the value used by
// fairwayctl and crewctl so all addons surface a hung binary uniformly.
const installedVersionTimeout = 5 * time.Second

// Sentinel errors returned by Installer methods. CLI handlers check these
// via errors.Is to distinguish actionable conditions from generic failures.
var (
	// ErrAlreadyInstalled is returned when the binary is already present at
	// the requested version.
	ErrAlreadyInstalled = errors.New("comms: already installed at current version")

	// ErrUpgradeRequired is returned when a different version is installed
	// and Force is not set; the caller should run `shipyard update`.
	ErrUpgradeRequired = errors.New("comms: different version installed — run 'shipyard update'")

	// ErrAlreadyAtVersion is returned by Upgrade when the installed version
	// matches the target version (nothing to do).
	ErrAlreadyAtVersion = errors.New("comms: already at current version")
)

// HTTPClient is the interface for making HTTP requests. Injected for tests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Installer handles download, verification and installation of the
// shipyard-comms binary. Unlike fairway/crew, comms in v1 is stateless —
// there is NO service registration, NO socket, NO pidfile. The installer
// only manages the binary itself and (optionally on uninstall) the state
// directory under StateDir.
//
// All fields are required unless noted.
type Installer struct {
	Version     string
	Platform    Platform
	BinDir      string // typically ~/.local/bin/
	StateDir    string // ~/.shipyard/comms/ — state preserved on uninstall by default
	Force       bool
	Purge       bool // remove StateDir on uninstall
	HTTPClient  HTTPClient
	ReleaseBase string
	Now         func() time.Time

	// rename is os.Rename by default; overridable in tests.
	rename func(old, new string) error
}

// BinPath returns the full path to the installed comms binary.
func (i *Installer) BinPath() string {
	return filepath.Join(i.BinDir, BinaryName)
}

// renameFn returns the rename function, defaulting to os.Rename.
func (i *Installer) renameFn() func(string, string) error {
	if i.rename != nil {
		return i.rename
	}
	return os.Rename
}

// IsInstalled reports whether a usable binary file exists at BinPath().
// It does NOT execute the binary — use InstalledVersion to confirm the
// binary also responds to --version. Directories at BinPath are treated
// as "not installed" because they cannot be executed.
func (i *Installer) IsInstalled() bool {
	info, err := os.Stat(i.BinPath())
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// InstalledVersion executes the installed binary with --version and
// returns the parsed semver string. Returns an error if the binary is
// absent or exec fails (including timeout). Callers that only need to
// know whether a binary is present (without running it) should prefer
// IsInstalled.
func (i *Installer) InstalledVersion() (string, error) {
	binPath := i.BinPath()
	if _, err := os.Stat(binPath); errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("comms: binary not found at %s", binPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), installedVersionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, binPath, "--version").Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("comms: exec --version timed out after %s", installedVersionTimeout)
		}
		return "", fmt.Errorf("comms: exec --version: %w", err)
	}
	return parseVersionOutput(string(out)), nil
}

// parseVersionOutput extracts the version token from `shipyard-comms
// --version` output. The binary emits just the version on its own line
// when --version is passed (matching its run() handler), so the trimmed
// input is the version. We keep the "shipyard-comms <version> …" prefix
// stripper as well in case a future caller pipes the default header.
func parseVersionOutput(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "shipyard-comms ") {
		parts := strings.Fields(trimmed)
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return trimmed
}

// Install downloads, verifies and installs shipyard-comms. The flow:
//  1. If not Force, check installed version: same → ErrAlreadyInstalled,
//     different → ErrUpgradeRequired.
//  2. Download artifact + checksum manifest from the GitHub release.
//  3. Verify SHA-256 against the manifest.
//  4. Extract the binary from the tarball into a temp file.
//  5. Chmod 0755 and rename into BinDir/shipyard-comms.
//
// There is no service registration step — comms is stateless.
func (i *Installer) Install(ctx context.Context) error {
	if !i.Force {
		installed, err := i.InstalledVersion()
		if err == nil {
			if installed == i.Version {
				return ErrAlreadyInstalled
			}
			return fmt.Errorf("%w (installed: %s, want: %s)", ErrUpgradeRequired, installed, i.Version)
		}
	}

	artifact := ArtifactName(i.Version, i.Platform)
	tag := ReleaseTag(i.Version)
	artifactURL := fmt.Sprintf("%s/%s/%s", i.ReleaseBase, tag, artifact)
	checksumName := ChecksumManifestName(i.Version)
	checksumURL := fmt.Sprintf("%s/%s/%s", i.ReleaseBase, tag, checksumName)

	tmpArtifact, err := i.Download(ctx, artifactURL)
	if err != nil {
		return fmt.Errorf("comms: download artifact: %w", err)
	}
	defer os.Remove(tmpArtifact)

	tmpChecksums, err := i.Download(ctx, checksumURL)
	if err != nil {
		return fmt.Errorf("comms: download checksums: %w", err)
	}
	defer os.Remove(tmpChecksums)

	expectedSHA, err := extractSHA(tmpChecksums, artifact)
	if err != nil {
		return fmt.Errorf("comms: checksum lookup in %s: %w", checksumName, err)
	}

	if err := VerifyChecksum(tmpArtifact, expectedSHA); err != nil {
		return fmt.Errorf("comms: checksum: %w", err)
	}

	if err := os.MkdirAll(i.BinDir, 0o755); err != nil {
		return fmt.Errorf("comms: create bin dir: %w", err)
	}

	tmpBin, err := os.CreateTemp(i.BinDir, ".shipyard-comms-*")
	if err != nil {
		return fmt.Errorf("comms: create temp: %w", err)
	}
	tmpBinPath := tmpBin.Name()
	tmpBin.Close()
	defer os.Remove(tmpBinPath)

	if err := ExtractBinary(tmpArtifact, tmpBinPath); err != nil {
		return fmt.Errorf("comms: extract: %w", err)
	}

	if err := os.Chmod(tmpBinPath, 0o755); err != nil {
		return fmt.Errorf("comms: chmod: %w", err)
	}

	dest := i.BinPath()
	if err := i.renameFn()(tmpBinPath, dest); err != nil {
		return fmt.Errorf("comms: install binary: %w", err)
	}

	return nil
}

// Uninstall removes the comms binary and, when Purge is set, the state
// directory under StateDir. All steps are attempted even if earlier ones
// fail; the first non-ignorable error is returned. Missing files are not
// errors — uninstall is idempotent.
func (i *Installer) Uninstall(ctx context.Context) error {
	return i.uninstall(ctx, i.Purge)
}

func (i *Installer) uninstall(_ context.Context, purge bool) error {
	var firstErr error

	if err := os.Remove(i.BinPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		firstErr = fmt.Errorf("comms: remove binary: %w", err)
	}

	if purge && i.StateDir != "" {
		if err := os.RemoveAll(i.StateDir); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("comms: purge state dir: %w", err)
		}
	}

	return firstErr
}

// Upgrade uninstalls the current comms (preserving state) and reinstalls
// at the target version. Returns ErrAlreadyAtVersion when versions match.
func (i *Installer) Upgrade(ctx context.Context) error {
	current, err := i.InstalledVersion()
	if err == nil && current == i.Version {
		return ErrAlreadyAtVersion
	}

	if err := i.uninstall(ctx, false); err != nil {
		return fmt.Errorf("comms: upgrade uninstall: %w", err)
	}

	if err := i.Install(ctx); err != nil {
		return fmt.Errorf("comms: upgrade install failed — run 'shipyard comms install' to recover: %w", err)
	}

	return nil
}

// Download fetches url via HTTPClient, streams the body to a temp file
// and returns the temp file path. The caller is responsible for removing
// the temp file.
func (i *Installer) Download(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "shipyard/"+i.Version)

	resp, err := i.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	tmp, err := os.CreateTemp("", "shipyard-comms-dl-*")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}

	return tmp.Name(), nil
}

// VerifyChecksum computes SHA-256 of the file at path and compares it to
// expected (hex-encoded). Returns nil on match.
func VerifyChecksum(path, expected string) error {
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

// ExtractBinary extracts the file named "shipyard-comms" from a .tar.gz
// archive and writes it to dest (overwriting).
func ExtractBinary(tarGzPath, dest string) error {
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
	return errors.New("shipyard-comms binary not found in archive")
}

// extractSHA finds the SHA-256 hex string for artifactName in a checksum
// manifest file. Lines have the format: <sha256hex>  <filename>
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
	return "", fmt.Errorf("no checksum found for %q", artifactName)
}

// ── Latest version discovery ─────────────────────────────────────────────────

type releaseListItem struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// ResolveLatestCommsVersion fetches the latest stable comms release
// version from GitHub by looking for tags prefixed with "comms-v". Used
// by `shipyard update` flows that want to follow `latest` without a
// hard-coded version.
func ResolveLatestCommsVersion(ctx context.Context, client HTTPClient) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, commsReleasesAPI, nil)
	if err != nil {
		return "", fmt.Errorf("comms: create version request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("comms: request latest version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("comms: request latest version: unexpected status %s", resp.Status)
	}

	var releases []releaseListItem
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", fmt.Errorf("comms: decode version response: %w", err)
	}

	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		if strings.HasPrefix(r.TagName, "comms-v") {
			version := strings.TrimPrefix(r.TagName, "comms-v")
			if version != "" {
				return version, nil
			}
		}
	}

	return "", errors.New("comms: no stable release found")
}
