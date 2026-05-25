package commsctl

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveBinary_FindsInLocalBin(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	binDir := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	binPath := filepath.Join(binDir, BinaryName)
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ResolveBinary()
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if got != binPath {
		t.Errorf("got %q, want %q", got, binPath)
	}
}

func TestResolveBinary_NotInstalled(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Empty PATH so exec.LookPath also fails.
	t.Setenv("PATH", "")

	_, err := ResolveBinary()
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled, got %v", err)
	}
}

func TestResolveBinary_RejectsDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("PATH", "")

	// Create a directory where the binary would be — must not match.
	binDir := filepath.Join(tmp, ".local", "bin", BinaryName)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := ResolveBinary()
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled when path is a directory, got %v", err)
	}
}

func TestReleaseTag(t *testing.T) {
	if got, want := ReleaseTag("0.1.0"), "comms-v0.1.0"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestArtifactName(t *testing.T) {
	got := ArtifactName("0.1.0", Platform{OS: "darwin", Arch: "arm64"})
	want := "shipyard-comms_0.1.0_darwin_arm64.tar.gz"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestChecksumManifestName(t *testing.T) {
	got := ChecksumManifestName("0.1.0")
	want := "shipyard-comms_0.1.0_checksums.txt"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCurrentPlatform(t *testing.T) {
	got := CurrentPlatform()
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Errorf("CurrentPlatform() = %+v, want {%s %s}", got, runtime.GOOS, runtime.GOARCH)
	}
}
