package update

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/internal/metadata"
)

const (
	owner          = "shipyard-auto"
	repo           = "shipyard"
	latestRelease  = "https://api.github.com/repos/" + owner + "/" + repo + "/releases/latest"
	archivePattern = "https://github.com/%s/%s/releases/download/%s/shipyard_%s_%s_%s.tar.gz"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Service struct {
	Client HTTPClient
	Now    func() time.Time
}

type Result struct {
	CurrentVersion string
	LatestVersion  string
	TargetPath     string
	Updated        bool
}

type latestReleaseResponse struct {
	TagName string `json:"tag_name"`
}

func NewService() Service {
	return Service{
		Client: http.DefaultClient,
		Now:    time.Now,
	}
}

func (s Service) Run(currentVersion, executablePath string) (Result, error) {
	latestVersion, err := s.resolveLatestVersion()
	if err != nil {
		return Result{}, err
	}

	result := Result{
		CurrentVersion: currentVersion,
		LatestVersion:  latestVersion,
		TargetPath:     executablePath,
	}

	if compareVersions(normalizeVersion(currentVersion), latestVersion) >= 0 {
		return result, nil
	}

	targetPath, err := s.resolveTargetPath(executablePath)
	if err != nil {
		return Result{}, err
	}
	result.TargetPath = targetPath

	if err := s.downloadAndInstall(latestVersion, targetPath); err != nil {
		return Result{}, err
	}

	if err := s.updateManifest(targetPath, latestVersion); err != nil {
		return Result{}, err
	}

	result.Updated = true
	return result, nil
}

func (s Service) resolveLatestVersion() (string, error) {
	req, err := http.NewRequest(http.MethodGet, latestRelease, nil)
	if err != nil {
		return "", fmt.Errorf("create latest release request: %w", err)
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request latest release: unexpected status %s", resp.Status)
	}

	var payload latestReleaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode latest release response: %w", err)
	}

	version := normalizeVersion(payload.TagName)
	if version == "" {
		return "", errors.New("latest release response did not contain a tag_name")
	}

	return version, nil
}

func (s Service) resolveTargetPath(executablePath string) (string, error) {
	shipyardHome, err := metadata.DefaultHomeDir()
	if err != nil {
		return "", err
	}

	manifest, err := metadata.Read(shipyardHome)
	if err == nil && manifest.BinaryPath != "" {
		return manifest.BinaryPath, nil
	}

	cleanPath, err := filepath.EvalSymlinks(executablePath)
	if err != nil {
		return filepath.Clean(executablePath), nil
	}

	return cleanPath, nil
}

func (s Service) downloadAndInstall(version, targetPath string) error {
	platformOS, platformArch, err := detectPlatform()
	if err != nil {
		return err
	}

	url := fmt.Sprintf(archivePattern, owner, repo, "v"+version, version, platformOS, platformArch)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create archive request: %w", err)
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("download release archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download release archive: unexpected status %s", resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create target directory: %w", err)
	}

	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("open release archive: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	tempFile, err := os.CreateTemp(filepath.Dir(targetPath), "shipyard-update-*")
	if err != nil {
		return fmt.Errorf("create temporary binary: %w", err)
	}

	tempPath := tempFile.Name()
	success := false
	defer func() {
		tempFile.Close()
		if !success {
			_ = os.Remove(tempPath)
		}
	}()

	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read release archive: %w", err)
		}

		if filepath.Base(header.Name) != "shipyard" {
			continue
		}

		if _, err := io.Copy(tempFile, tr); err != nil {
			return fmt.Errorf("write temporary binary: %w", err)
		}

		if err := tempFile.Close(); err != nil {
			return fmt.Errorf("close temporary binary: %w", err)
		}

		if err := os.Chmod(tempPath, 0o755); err != nil {
			return fmt.Errorf("chmod temporary binary: %w", err)
		}

		if err := os.Rename(tempPath, targetPath); err != nil {
			return fmt.Errorf("replace shipyard binary: %w", err)
		}

		success = true
		return nil
	}

	return errors.New("release archive did not contain the shipyard binary")
}

func (s Service) updateManifest(targetPath, version string) error {
	shipyardHome, err := metadata.DefaultHomeDir()
	if err != nil {
		return err
	}

	manifest := metadata.InstallManifest{
		Version:     version,
		BinaryPath:  targetPath,
		HomeDir:     shipyardHome,
		InstalledAt: s.Now().UTC(),
	}

	if err := metadata.Write(shipyardHome, manifest); err != nil {
		return err
	}

	return nil
}

func detectPlatform() (string, string, error) {
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "amd64"
	case "arm64":
		arch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}

	switch runtime.GOOS {
	case "linux", "darwin":
		return runtime.GOOS, arch, nil
	default:
		return "", "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

func normalizeVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

func compareVersions(left, right string) int {
	leftParts := versionParts(left)
	rightParts := versionParts(right)

	maxLen := len(leftParts)
	if len(rightParts) > maxLen {
		maxLen = len(rightParts)
	}

	for i := 0; i < maxLen; i++ {
		var l, r int
		if i < len(leftParts) {
			l = leftParts[i]
		}
		if i < len(rightParts) {
			r = rightParts[i]
		}

		switch {
		case l < r:
			return -1
		case l > r:
			return 1
		}
	}

	return 0
}

func versionParts(version string) []int {
	raw := strings.Split(version, ".")
	parts := make([]int, 0, len(raw))

	for _, part := range raw {
		value, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		parts = append(parts, value)
	}

	return parts
}
