package crew

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/crewctl"
)

func TestRequireInstalled_Missing(t *testing.T) {
	prev := resolveBinaryFn
	resolveBinaryFn = func() (string, error) { return "", crewctl.ErrNotInstalled }
	t.Cleanup(func() { resolveBinaryFn = prev })

	err := requireInstalled(nil, nil)
	if err == nil {
		t.Fatal("expected error when binary is missing")
	}
	if !errors.Is(err, ErrAddonNotInstalled) {
		t.Fatalf("want ErrAddonNotInstalled, got %v", err)
	}
	if !strings.Contains(err.Error(), "shipyard crew install") {
		t.Fatalf("error must hint at install command: %v", err)
	}
}

func TestRequireInstalled_Present(t *testing.T) {
	prev := resolveBinaryFn
	resolveBinaryFn = func() (string, error) { return "/fake/shipyard-crew", nil }
	t.Cleanup(func() { resolveBinaryFn = prev })

	if err := requireInstalled(nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestResolveBinary_FindsLocalBin writes a fake binary under
// $HOME/.local/bin and confirms ResolveBinary returns its path.
func TestResolveBinary_FindsLocalBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-specific layout")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "") // ensure LookPath fallback cannot succeed

	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, crewctl.BinaryName)
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := crewctl.ResolveBinary()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != binPath {
		t.Fatalf("got %q, want %q", got, binPath)
	}
}

func TestResolveBinary_MissingReturnsErrNotInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-specific layout")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "empty"))

	_, err := crewctl.ResolveBinary()
	if !errors.Is(err, crewctl.ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}
}
