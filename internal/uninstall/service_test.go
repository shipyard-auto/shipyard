package uninstall

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/internal/metadata"
)

type fakeRemover struct {
	removedFiles []string
	removedDirs  []string
}

func (f *fakeRemover) RemoveFile(path string) error {
	f.removedFiles = append(f.removedFiles, path)
	return nil
}

func (f *fakeRemover) RemoveAll(path string) error {
	f.removedDirs = append(f.removedDirs, path)
	return nil
}

func TestRunUsesManifestBinaryPath(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	shipyardHome := filepath.Join(tempHome, ".shipyard")
	if err := metadata.Write(shipyardHome, metadata.InstallManifest{
		Version:     "0.1.0-dev",
		BinaryPath:  "/tmp/shipyard",
		HomeDir:     shipyardHome,
		InstalledAt: time.Date(2026, time.April, 14, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("metadata.Write() error = %v", err)
	}

	remover := &fakeRemover{}
	service := Service{Remover: remover}

	result, err := service.Run("/fallback/shipyard")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !result.ManifestPresent {
		t.Fatalf("Result.ManifestPresent = false, want true")
	}

	if len(remover.removedFiles) != 1 || remover.removedFiles[0] != "/tmp/shipyard" {
		t.Fatalf("removedFiles = %#v, want manifest binary path", remover.removedFiles)
	}

	if len(remover.removedDirs) != 1 || remover.removedDirs[0] != shipyardHome {
		t.Fatalf("removedDirs = %#v, want %#v", remover.removedDirs, []string{shipyardHome})
	}
}

func TestRunFallsBackToExplicitBinaryPath(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	remover := &fakeRemover{}
	service := Service{Remover: remover}

	result, err := service.Run("/fallback/shipyard")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.ManifestPresent {
		t.Fatalf("Result.ManifestPresent = true, want false")
	}

	if len(remover.removedFiles) != 1 || remover.removedFiles[0] != "/fallback/shipyard" {
		t.Fatalf("removedFiles = %#v, want explicit binary path", remover.removedFiles)
	}
}

func TestRunFailsOnManifestParseError(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	shipyardHome := filepath.Join(tempHome, ".shipyard")
	if err := os.MkdirAll(shipyardHome, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}

	manifestPath := filepath.Join(shipyardHome, "install.json")
	if err := os.WriteFile(manifestPath, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	service := Service{Remover: &fakeRemover{}}

	_, err := service.Run("/fallback/shipyard")
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}

	if !errors.Is(err, os.ErrInvalid) && err.Error() == "" {
		t.Fatalf("Run() returned unexpected empty error")
	}
}
