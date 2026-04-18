package fairwayctl

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
	"runtime"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/internal/service"
)

// DefaultReleaseBase is the GitHub releases base URL.
const DefaultReleaseBase = "https://github.com/shipyard-auto/shipyard/releases/download"

// ReleaseTag returns the GitHub release tag used by fairway artifacts.
func ReleaseTag(version string) string {
	return fmt.Sprintf("fairway-v%s", version)
}

// ChecksumManifestName returns the checksum manifest file name for a fairway release.
func ChecksumManifestName(version string) string {
	return fmt.Sprintf("shipyard-fairway_%s_checksums.txt", version)
}

// ErrAlreadyInstalled is returned when the binary exists at the current version.
var ErrAlreadyInstalled = errors.New("fairway: already installed at current version")

// ErrUpgradeRequired is returned when a different version is already installed.
var ErrUpgradeRequired = errors.New("fairway: different version installed — run 'shipyard update'")

const fairwayReleasesAPI = "https://api.github.com/repos/shipyard-auto/shipyard/releases?per_page=30"

type releaseListItem struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// ResolveLatestFairwayVersion fetches the latest stable fairway release version from GitHub.
func ResolveLatestFairwayVersion(ctx context.Context, client HTTPClient) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fairwayReleasesAPI, nil)
	if err != nil {
		return "", fmt.Errorf("fairway: create version request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fairway: request latest version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fairway: request latest version: unexpected status %s", resp.Status)
	}

	var releases []releaseListItem
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", fmt.Errorf("fairway: decode version response: %w", err)
	}

	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		if strings.HasPrefix(r.TagName, "fairway-v") {
			version := strings.TrimPrefix(r.TagName, "fairway-v")
			if version != "" {
				return version, nil
			}
		}
	}

	return "", errors.New("fairway: no stable release found")
}

// HTTPClient is the interface for making HTTP requests. Injected for tests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// ServiceAdder abstracts Shipyard service registration for fairway.
type ServiceAdder interface {
	AddFairway(execPath string) error
	RemoveFairway() error
	IsFairwayInstalled() (bool, error)
}

// Platform identifies the target OS and CPU architecture.
type Platform struct {
	OS   string
	Arch string
}

// CurrentPlatform returns the platform for the running process.
func CurrentPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// ErrAlreadyAtVersion is returned by Upgrade when the installed version matches
// the core version.
var ErrAlreadyAtVersion = errors.New("fairway: already at current version")

// Installer handles download, verification, and service registration for
// the shipyard-fairway addon. All fields are required unless noted.
type Installer struct {
	Version      string
	Platform     Platform
	BinDir       string
	StateDir     string // ~/.shipyard/fairway/ — state preserved on uninstall by default
	RunDir       string // ~/.shipyard/run/     — socket and pidfile location
	Force        bool
	Purge        bool // remove StateDir on uninstall
	HTTPClient   HTTPClient
	ReleaseBase  string
	Now          func() time.Time
	ServiceAdder ServiceAdder

	// rename is os.Rename by default; overridable in tests.
	rename func(old, new string) error
}

// BinPath returns the full path to the installed fairway binary.
func (i *Installer) BinPath() string {
	return filepath.Join(i.BinDir, "shipyard-fairway")
}

// renameFn returns the rename function, defaulting to os.Rename.
func (i *Installer) renameFn() func(string, string) error {
	if i.rename != nil {
		return i.rename
	}
	return os.Rename
}

// ArtifactName returns the release archive name for a given version and platform.
func ArtifactName(version string, p Platform) string {
	return fmt.Sprintf("shipyard-fairway_%s_%s_%s.tar.gz", version, p.OS, p.Arch)
}

// InstalledVersion executes the installed binary with --version and returns
// the parsed semver string (e.g. "1.0.5"). Returns an error if the binary is
// absent or exec fails.
func (i *Installer) InstalledVersion() (string, error) {
	binPath := i.BinPath()
	if _, err := os.Stat(binPath); errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("fairway: binary not found at %s", binPath)
	}
	out, err := exec.Command(binPath, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("fairway: exec --version: %w", err)
	}
	return parseVersionOutput(string(out)), nil
}

// parseVersionOutput extracts the semver token from `shipyard-fairway --version`
// output, which has the form: "shipyard-fairway <version> (<hash>, built <ts>)".
// Falls back to the trimmed input when the prefix is missing.
func parseVersionOutput(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "shipyard-fairway ") {
		parts := strings.Fields(trimmed)
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return trimmed
}

// Install downloads, verifies, installs and registers shipyard-fairway.
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
		return fmt.Errorf("fairway: download artifact: %w", err)
	}
	defer os.Remove(tmpArtifact)

	tmpChecksums, err := i.Download(ctx, checksumURL)
	if err != nil {
		return fmt.Errorf("fairway: download checksums: %w", err)
	}
	defer os.Remove(tmpChecksums)

	expectedSHA, err := extractSHA(tmpChecksums, artifact)
	if err != nil {
		return fmt.Errorf("fairway: checksum lookup in %s: %w", checksumName, err)
	}

	if err := VerifyChecksum(tmpArtifact, expectedSHA); err != nil {
		return fmt.Errorf("fairway: checksum: %w", err)
	}

	if err := os.MkdirAll(i.BinDir, 0755); err != nil {
		return fmt.Errorf("fairway: create bin dir: %w", err)
	}

	tmpBin, err := os.CreateTemp(i.BinDir, ".shipyard-fairway-*")
	if err != nil {
		return fmt.Errorf("fairway: create temp: %w", err)
	}
	tmpBinPath := tmpBin.Name()
	tmpBin.Close()
	defer os.Remove(tmpBinPath)

	if err := ExtractBinary(tmpArtifact, tmpBinPath); err != nil {
		return fmt.Errorf("fairway: extract: %w", err)
	}

	if err := os.Chmod(tmpBinPath, 0755); err != nil {
		return fmt.Errorf("fairway: chmod: %w", err)
	}

	dest := i.BinPath()
	if err := i.renameFn()(tmpBinPath, dest); err != nil {
		return fmt.Errorf("fairway: install binary: %w", err)
	}

	if err := i.ServiceAdder.AddFairway(dest); err != nil {
		os.Remove(dest)
		return fmt.Errorf("fairway: register service: %w", err)
	}

	return nil
}

// Uninstall removes the fairway service and binary.
// Uninstall stops and removes the fairway service, binary, run-dir artifacts,
// and optionally the state directory. All steps are attempted even if earlier
// ones fail; the first non-ignorable error is returned.
func (i *Installer) Uninstall(ctx context.Context) error {
	return i.uninstall(ctx, i.Purge)
}

func (i *Installer) uninstall(ctx context.Context, purge bool) error {
	var firstErr error

	if err := i.ServiceAdder.RemoveFairway(); err != nil {
		firstErr = fmt.Errorf("fairway: remove service (warning): %w", err)
	}

	if err := os.Remove(i.BinPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		if firstErr == nil {
			firstErr = fmt.Errorf("fairway: remove binary: %w", err)
		}
	}

	if purge && i.StateDir != "" {
		if err := os.RemoveAll(i.StateDir); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("fairway: purge state dir: %w", err)
		}
	}

	if i.RunDir != "" {
		_ = os.Remove(filepath.Join(i.RunDir, "fairway.sock"))
		_ = os.Remove(filepath.Join(i.RunDir, "fairway.pid"))
	}

	return firstErr
}

// Upgrade uninstalls the current fairway (preserving state) and reinstalls at
// the core version. Returns ErrAlreadyAtVersion when versions match.
func (i *Installer) Upgrade(ctx context.Context) error {
	current, err := i.InstalledVersion()
	if err == nil && current == i.Version {
		return ErrAlreadyAtVersion
	}

	if err := i.uninstall(ctx, false); err != nil {
		return fmt.Errorf("fairway: upgrade uninstall: %w", err)
	}

	if err := i.Install(ctx); err != nil {
		return fmt.Errorf("fairway: upgrade install failed — run 'shipyard fairway install' to recover: %w", err)
	}

	return nil
}

// Download fetches url via HTTPClient, streams the body to a temp file, and
// returns the temp file path. The caller is responsible for removing it.
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

	tmp, err := os.CreateTemp("", "shipyard-fairway-dl-*")
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

// VerifyChecksum computes SHA-256 of the file at path and compares it to expected.
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

// ExtractBinary extracts the file named "shipyard-fairway" from a .tar.gz
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
		if filepath.Base(hdr.Name) != "shipyard-fairway" {
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
	return errors.New("shipyard-fairway binary not found in archive")
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

// ── Default ServiceAdder backed by internal/service ──────────────────────────

const fairwayServiceName = "fairway"

// NewServiceAdder creates the default ServiceAdder backed by the Shipyard
// service system.
func NewServiceAdder() (ServiceAdder, error) {
	svc, err := service.NewService()
	if err != nil {
		return nil, err
	}
	return &serviceAdderImpl{svc: svc}, nil
}

type serviceAdderImpl struct {
	svc service.Service
}

func (a *serviceAdderImpl) AddFairway(execPath string) error {
	name := strPtr(fairwayServiceName)
	cmd := strPtr(execPath)
	enabled := boolPtr(true)
	autoRestart := boolPtr(true)
	_, err := a.svc.Add(service.ServiceInput{
		Name:        name,
		Command:     cmd,
		Enabled:     enabled,
		AutoRestart: autoRestart,
	})
	return err
}

func (a *serviceAdderImpl) IsFairwayInstalled() (bool, error) {
	records, err := a.svc.List()
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if r.Name == fairwayServiceName {
			return true, nil
		}
	}
	return false, nil
}

func (a *serviceAdderImpl) RemoveFairway() error {
	records, err := a.svc.List()
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Name == fairwayServiceName {
			return a.svc.Delete(r.ID)
		}
	}
	return nil
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
