package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
)

// ── CLI test helpers ──────────────────────────────────────────────────────────

// errHTTPClient is an HTTPClient that always returns a transport error.
type errHTTPClient struct{}

func (e *errHTTPClient) Do(_ *http.Request) (*http.Response, error) {
	return nil, errors.New("stub: no network in tests")
}

// noopServiceAdder is a ServiceAdder that does nothing and never errors.
type noopServiceAdder struct{}

func (n *noopServiceAdder) AddFairway(_ string) error         { return nil }
func (n *noopServiceAdder) RemoveFairway() error              { return nil }
func (n *noopServiceAdder) IsFairwayInstalled() (bool, error) { return false, nil }

// newCLIInstaller builds a minimal Installer suitable for CLI tests.
// The BinDir is set to dir; no real network or service calls are made.
func newCLIInstaller(version, dir string) *fairwayctl.Installer {
	return &fairwayctl.Installer{
		Version:      version,
		Platform:     fairwayctl.Platform{OS: "linux", Arch: "amd64"},
		BinDir:       dir,
		HTTPClient:   &errHTTPClient{},
		ReleaseBase:  "https://fake.example/releases/download",
		ServiceAdder: &noopServiceAdder{},
	}
}

// writeFakeBinary writes a shell script to dir/shipyard-fairway that echoes version.
func writeFakeBinary(t *testing.T, dir, version string) {
	t.Helper()
	path := filepath.Join(dir, "shipyard-fairway")
	content := "#!/bin/sh\necho '" + version + "'\n"
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestFairwayInstallCommand_noArgs_runsInstall(t *testing.T) {
	dir := t.TempDir()
	// Binary already installed at same version → ErrAlreadyInstalled → exit 0.
	writeFakeBinary(t, dir, "0.22")
	inst := newCLIInstaller("0.22", dir)

	cmd := newFairwayInstallCmdWith(inst)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "already installed") {
		t.Errorf("expected 'already installed' in output, got: %q", buf.String())
	}
}

func TestFairwayInstallCommand_forceFlag_reinstalls(t *testing.T) {
	dir := t.TempDir()
	// Even though the binary is present and version matches, --force bypasses
	// the check. The stub HTTP client will fail the download, confirming the
	// install path was entered (not short-circuited by "already installed").
	writeFakeBinary(t, dir, "0.22")
	inst := newCLIInstaller("0.22", dir)

	cmd := newFairwayInstallCmdWith(inst)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)

	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}

	// Execute returns an error (stub HTTP failure) — that's expected.
	_ = cmd.Execute()

	out := buf.String()
	if strings.Contains(out, "already installed") {
		t.Error("--force should bypass 'already installed' message")
	}
}

func TestFairwayInstallCommand_progressMessagesAppear(t *testing.T) {
	dir := t.TempDir()
	// Binary absent → download will fail, but header must appear before the error.
	inst := newCLIInstaller("0.22", dir)

	cmd := newFairwayInstallCmdWith(inst)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	_ = cmd.Execute()

	out := buf.String()
	if !strings.Contains(out, "SHIPYARD FAIRWAY") {
		t.Errorf("expected section title in output, got: %q", out)
	}
	if !strings.Contains(out, "Installing") {
		t.Errorf("expected 'Installing' progress in output, got: %q", out)
	}
}

func TestFairwayUninstallCommand_defaultKeepsState(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	inst := newCLIInstaller("0.22", dir)
	inst.StateDir = stateDir

	cmd := newFairwayUninstallCmdWith(inst)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// State dir must still exist (no --purge).
	if _, err := os.Stat(stateDir); os.IsNotExist(err) {
		t.Error("state dir should be preserved without --purge")
	}
	out := buf.String()
	if !strings.Contains(out, "removed") {
		t.Errorf("expected 'removed' in output, got: %q", out)
	}
}

func TestFairwayUninstallCommand_purgeFlagRemovesState(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	inst := newCLIInstaller("0.22", dir)
	inst.StateDir = stateDir

	cmd := newFairwayUninstallCmdWith(inst)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)

	if err := cmd.Flags().Set("purge", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// State dir must be gone.
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Error("state dir should be removed with --purge")
	}
	out := buf.String()
	if !strings.Contains(out, "purged") {
		t.Errorf("expected 'purged' in output, got: %q", out)
	}
}
