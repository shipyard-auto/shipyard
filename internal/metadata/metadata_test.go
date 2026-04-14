package metadata

import (
	"path/filepath"
	"testing"
	"time"
)

func TestManifestPath(t *testing.T) {
	t.Parallel()

	got := ManifestPath("/tmp/.shipyard")
	want := filepath.Join("/tmp/.shipyard", "install.json")

	if got != want {
		t.Fatalf("ManifestPath() = %q, want %q", got, want)
	}
}

func TestWriteAndRead(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	shipyardHome := filepath.Join(tempDir, ".shipyard")
	now := time.Date(2026, time.April, 14, 12, 0, 0, 0, time.UTC)

	want := InstallManifest{
		Version:     "0.1.0-dev",
		BinaryPath:  "/tmp/bin/shipyard",
		HomeDir:     shipyardHome,
		InstalledAt: now,
	}

	if err := Write(shipyardHome, want); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got, err := Read(shipyardHome)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if got != want {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
}
