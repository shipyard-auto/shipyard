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

	"github.com/shipyard-auto/shipyard/internal/commsctl"
)

// errCommsHTTPClient always returns a transport error — exercises the
// install path without making a real network request.
type errCommsHTTPClient struct{}

func (e *errCommsHTTPClient) Do(_ *http.Request) (*http.Response, error) {
	return nil, errors.New("stub: no network in tests")
}

func newCommsCLIInstaller(version, binDir, stateDir string) *commsctl.Installer {
	return &commsctl.Installer{
		Version:     version,
		Platform:    commsctl.Platform{OS: "linux", Arch: "amd64"},
		BinDir:      binDir,
		StateDir:    stateDir,
		HTTPClient:  &errCommsHTTPClient{},
		ReleaseBase: "https://fake.example/releases/download",
	}
}

func writeFakeCommsBinary(t *testing.T, dir, version string) {
	t.Helper()
	path := filepath.Join(dir, commsctl.BinaryName)
	content := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '" + version + "'; exit 0; fi\necho stub\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// isolateHomeForTest redirects $HOME so the registry write inside install
// command doesn't touch the developer's real ~/.shipyard.
func isolateHomeForTest(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestCommsInstallCommand_alreadyInstalled(t *testing.T) {
	isolateHomeForTest(t)
	binDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	writeFakeCommsBinary(t, binDir, "0.1.0")

	inst := newCommsCLIInstaller("0.1.0", binDir, stateDir)
	cmd := newCommsInstallCmdWith(inst)
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

func TestCommsInstallCommand_upgradeRequired_returnsError(t *testing.T) {
	isolateHomeForTest(t)
	binDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	writeFakeCommsBinary(t, binDir, "0.0.5")

	inst := newCommsCLIInstaller("0.1.0", binDir, stateDir)
	cmd := newCommsInstallCmdWith(inst)
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected ErrUpgradeRequired surfaced as error")
	}
	if !errors.Is(err, commsctl.ErrUpgradeRequired) {
		t.Errorf("expected ErrUpgradeRequired, got %v", err)
	}
}

func TestCommsInstallCommand_force_attemptsNetwork(t *testing.T) {
	isolateHomeForTest(t)
	binDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	writeFakeCommsBinary(t, binDir, "0.1.0")

	inst := newCommsCLIInstaller("0.1.0", binDir, stateDir)
	cmd := newCommsInstallCmdWith(inst)
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})

	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatalf("set --force: %v", err)
	}

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected stub network error, got nil")
	}
	if !strings.Contains(err.Error(), "no network") {
		t.Errorf("expected stub network error to surface, got %v", err)
	}
}

func TestCommsUninstallCommand_removesBinary(t *testing.T) {
	isolateHomeForTest(t)
	binDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	writeFakeCommsBinary(t, binDir, "0.1.0")

	inst := newCommsCLIInstaller("0.1.0", binDir, stateDir)
	cmd := newCommsUninstallCmdWith(inst)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "removed") {
		t.Errorf("expected confirmation in output, got: %q", buf.String())
	}
	if _, err := os.Stat(filepath.Join(binDir, commsctl.BinaryName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("binary still present after uninstall: %v", err)
	}
}

func TestCommsUninstallCommand_purgeRemovesStateDir(t *testing.T) {
	isolateHomeForTest(t)
	binDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "channels.json"), []byte("[]"), 0o600); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	inst := newCommsCLIInstaller("0.1.0", binDir, stateDir)
	cmd := newCommsUninstallCmdWith(inst)
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})

	if err := cmd.Flags().Set("purge", "true"); err != nil {
		t.Fatalf("set --purge: %v", err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state dir should be purged: stat err=%v", err)
	}
}

func TestCommsCmd_RegistersSubcommands(t *testing.T) {
	cmd := newCommsCmd()
	want := map[string]bool{
		"install":   true,
		"update":    true,
		"uninstall": true,
		"status":    true,
		"channel":   true,
		"send":      true,
	}
	for _, sub := range cmd.Commands() {
		delete(want, sub.Name())
	}
	if len(want) > 0 {
		t.Errorf("missing subcommands: %v", want)
	}
}

func TestCommsUpdateCommand_AlreadyAtVersion(t *testing.T) {
	isolateHomeForTest(t)
	binDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	writeFakeCommsBinary(t, binDir, "0.2.0")

	inst := newCommsCLIInstaller("0.2.0", binDir, stateDir)
	cmd := newCommsUpdateCmdWith(inst)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "already up to date") {
		t.Errorf("expected 'already up to date' message, got: %q", buf.String())
	}
}

func TestCommsUpdateCommand_UpgradeAttemptsNetwork(t *testing.T) {
	// When installed version differs from target, Upgrade triggers
	// Uninstall → Install. The stub HTTP client makes Install fail, but
	// that's enough proof the upgrade path was entered (not short-
	// circuited by ErrAlreadyAtVersion).
	isolateHomeForTest(t)
	binDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	writeFakeCommsBinary(t, binDir, "0.1.0")

	inst := newCommsCLIInstaller("0.2.0", binDir, stateDir)
	cmd := newCommsUpdateCmdWith(inst)
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected stub network error, got nil")
	}
	if !strings.Contains(err.Error(), "no network") {
		t.Errorf("expected stub network error to surface, got %v", err)
	}
}
