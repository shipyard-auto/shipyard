package crew

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/crewctl"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

func makeTarGz(t *testing.T, content []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{Name: crewctl.BinaryName, Typeflag: tar.TypeReg, Size: int64(len(content)), Mode: 0o755}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

type stubClient struct {
	responses map[string][]byte
	status    map[string]int
}

func (s *stubClient) Do(req *http.Request) (*http.Response, error) {
	body, ok := s.responses[req.URL.String()]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("not found")),
		}, nil
	}
	code := http.StatusOK
	if c, has := s.status[req.URL.String()]; has {
		code = c
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

// newFakeInstaller builds a crewctl.Installer wired to a fake HTTP client
// serving a fixture tarball whose extracted binary reports `version`.
func newFakeInstaller(t *testing.T, version string) *crewctl.Installer {
	t.Helper()
	base := t.TempDir()
	p := crewctl.Platform{OS: "linux", Arch: "amd64"}
	content := []byte(fmt.Sprintf("#!/bin/sh\necho 'shipyard-crew %s (test, built 2026-04-20)'\n", version))
	archive, sha := makeTarGz(t, content)
	artifact := crewctl.ArtifactName(version, p)
	root := "https://fake.example/releases/download"
	client := &stubClient{
		responses: map[string][]byte{
			root + "/" + crewctl.ReleaseTag(version) + "/" + artifact:                              archive,
			root + "/" + crewctl.ReleaseTag(version) + "/" + crewctl.ChecksumManifestName(version): []byte(sha + "  " + artifact + "\n"),
		},
	}
	return &crewctl.Installer{
		Version:     version,
		Platform:    p,
		BinDir:      filepath.Join(base, "bin"),
		StateDir:    filepath.Join(base, "state"),
		RunDir:      filepath.Join(base, "run"),
		HTTPClient:  client,
		ReleaseBase: root,
		Warn:        io.Discard,
	}
}

// runCmd captures stdout/stderr and executes the given cobra command.
func runCmd(t *testing.T, cmd interface {
	ExecuteContext(context.Context) error
	SetOut(io.Writer)
	SetErr(io.Writer)
	SetArgs([]string)
}, args ...string) (string, string, error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return outBuf.String(), errBuf.String(), err
}

// ── Install ───────────────────────────────────────────────────────────────────

func TestInstall_happyPath(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	cmd := newInstallCmdWith(inst)

	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "Installing shipyard-crew 0.1.0") {
		t.Errorf("missing install banner, got %q", out)
	}
	if !strings.Contains(out, "installed: "+inst.BinPath()) {
		t.Errorf("missing installed path line, got %q", out)
	}
	if _, err := os.Stat(inst.BinPath()); err != nil {
		t.Errorf("binary missing: %v", err)
	}
}

// TestInstall_emitsPATHWarningOnce verifies that a successful install routes
// the PATH warning through Installer.Warn exactly once — guarding against the
// regression where a duplicate CLI-layer maybeWarnPATH caused two warnings to
// reach stderr in production. The shell-specific content of the message is
// covered by crewctl.TestInstaller_maybeWarnPATH_*; here we only assert that
// the wiring still goes through the Installer's Warn writer.
func TestInstall_emitsPATHWarningOnce(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	var warn bytes.Buffer
	inst.Warn = &warn
	inst.Getenv = func(k string) string {
		switch k {
		case "PATH":
			return "/usr/bin:/bin"
		case "SHELL":
			return "/bin/zsh"
		}
		return ""
	}

	cmd := newInstallCmdWith(inst)
	if _, _, err := runCmd(t, cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := warn.String()
	if !strings.Contains(got, "not in your PATH") {
		t.Fatalf("expected PATH warning, got %q", got)
	}
	if strings.Count(got, "not in your PATH") != 1 {
		t.Fatalf("expected exactly one warning, got %d in %q", strings.Count(got, "not in your PATH"), got)
	}
}

func TestInstall_alreadyInstalled_noError(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	// Pre-install a binary that reports the same version.
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\necho 'shipyard-crew 0.1.0 (test, built 2026-04-20)'\n")
	if err := os.WriteFile(inst.BinPath(), content, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newInstallCmdWith(inst)
	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("expected nil (already installed), got %v", err)
	}
	if !strings.Contains(out, "already installed") {
		t.Errorf("missing already-installed notice: %q", out)
	}
}

func TestInstall_differentVersion_errorsWithHint(t *testing.T) {
	inst := newFakeInstaller(t, "0.2.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\necho 'shipyard-crew 0.1.0 (test, built 2026-04-20)'\n")
	if err := os.WriteFile(inst.BinPath(), content, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newInstallCmdWith(inst)
	out, _, err := runCmd(t, cmd)
	if !errors.Is(err, crewctl.ErrUpgradeRequired) {
		t.Errorf("want ErrUpgradeRequired, got %v", err)
	}
	if !strings.Contains(out, "--force") {
		t.Errorf("missing --force hint: %q", out)
	}
}

func TestInstall_forceFlag_overridesInstalledCheck(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-install same version but with different content to verify overwrite.
	if err := os.WriteFile(inst.BinPath(), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newInstallCmdWith(inst)
	_, _, err := runCmd(t, cmd, "--force")
	if err != nil {
		t.Fatalf("Install --force: %v", err)
	}
	got, _ := os.ReadFile(inst.BinPath())
	if string(got) == "old" {
		t.Error("--force should replace pre-existing binary")
	}
}

func TestInstall_versionFlag_overridesDefault(t *testing.T) {
	inst := newFakeInstaller(t, "0.3.0")
	cmd := newInstallCmdWith(inst)
	out, _, err := runCmd(t, cmd, "--version", "0.3.0")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "Installing shipyard-crew 0.3.0") {
		t.Errorf("version flag not honoured: %q", out)
	}
}

// ── Uninstall ─────────────────────────────────────────────────────────────────

func TestUninstall_yesFlag_noPrompt(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(inst.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(inst.StateDir, "keep.yaml")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newUninstallCmdWith(inst)
	out, _, err := runCmd(t, cmd, "--yes")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "uninstalled: "+inst.BinPath()) {
		t.Errorf("missing uninstalled line: %q", out)
	}
	if _, err := os.Stat(inst.BinPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("binary must be removed")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("state file must remain: %v", err)
	}
}

func TestUninstall_prompt_acceptsYes(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Force interactive path.
	prev := ttyIsInteractive
	ttyIsInteractive = func() bool { return true }
	t.Cleanup(func() { ttyIsInteractive = prev })

	cmd := newUninstallCmdWith(inst)
	cmd.SetIn(strings.NewReader("y\n"))

	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "uninstalled") {
		t.Errorf("expected uninstalled message, got %q", out)
	}
}

func TestUninstall_prompt_declines(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := ttyIsInteractive
	ttyIsInteractive = func() bool { return true }
	t.Cleanup(func() { ttyIsInteractive = prev })

	cmd := newUninstallCmdWith(inst)
	cmd.SetIn(strings.NewReader("n\n"))

	_, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("expected nil on decline, got %v", err)
	}
	if _, err := os.Stat(inst.BinPath()); err != nil {
		t.Errorf("binary must remain when user declines: %v", err)
	}
}

func TestUninstall_nonInteractive_withoutYes_fails(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	prev := ttyIsInteractive
	ttyIsInteractive = func() bool { return false }
	t.Cleanup(func() { ttyIsInteractive = prev })

	cmd := newUninstallCmdWith(inst)
	_, _, err := runCmd(t, cmd)
	if err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Errorf("want non-interactive error, got %v", err)
	}
}

func TestUninstall_warnsAboutRegisteredAgents(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(inst.StateDir, "watcher")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte("name: watcher"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newUninstallCmdWith(inst)
	out, _, err := runCmd(t, cmd, "--yes")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "watcher") || !strings.Contains(out, "fire") {
		t.Errorf("expected agent warning, got %q", out)
	}
}

// stubUnitFileMap installs a test-scoped unitFileForFn that resolves each
// configured agent name to a fixed path on disk, then restores the previous
// resolver via t.Cleanup. Tests that don't list an agent name receive ok=false.
func stubUnitFileMap(t *testing.T, mapping map[string]string) {
	t.Helper()
	prev := unitFileForFn
	unitFileForFn = func(name string) (string, bool) {
		path, ok := mapping[name]
		return path, ok
	}
	t.Cleanup(func() { unitFileForFn = prev })
}

// writeAgentYAML creates `<stateDir>/<name>/agent.yaml` with minimal content.
func writeAgentYAML(t *testing.T, stateDir, name string) string {
	t.Helper()
	dir := filepath.Join(stateDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("name: "+name), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDetectActiveServices_returnsOnlyAgentsWithUnitFile(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentYAML(t, stateDir, "alpha")
	writeAgentYAML(t, stateDir, "beta")
	writeAgentYAML(t, stateDir, "gamma")

	unitsDir := t.TempDir()
	betaUnit := filepath.Join(unitsDir, "beta.service")
	if err := os.WriteFile(betaUnit, []byte("[Unit]"), 0o644); err != nil {
		t.Fatal(err)
	}
	// alpha and gamma report a path but the file doesn't exist.
	resolver := func(name string) (string, bool) {
		return filepath.Join(unitsDir, name+".service"), true
	}

	got := detectActiveServices(stateDir, resolver)
	if len(got) != 1 || got[0] != "beta" {
		t.Fatalf("got %v, want [beta]", got)
	}
}

func TestDetectActiveServices_resolverNotOk_skips(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentYAML(t, stateDir, "watcher")

	got := detectActiveServices(stateDir, func(string) (string, bool) {
		return "", false
	})
	if len(got) != 0 {
		t.Fatalf("expected no active services when resolver returns ok=false, got %v", got)
	}
}

func TestUninstall_yesWithActiveService_aborts(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgentYAML(t, inst.StateDir, "watcher")

	// Mock unit file on disk for watcher.
	unitsDir := t.TempDir()
	unitPath := filepath.Join(unitsDir, "watcher.plist")
	if err := os.WriteFile(unitPath, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubUnitFileMap(t, map[string]string{"watcher": unitPath})

	cmd := newUninstallCmdWith(inst)
	_, _, err := runCmd(t, cmd, "--yes")
	if err == nil {
		t.Fatal("expected error when active service present without --force-services")
	}
	if !strings.Contains(err.Error(), "watcher") {
		t.Errorf("error must name the agent, got %v", err)
	}
	if !strings.Contains(err.Error(), "--force-services") {
		t.Errorf("error must mention --force-services escape, got %v", err)
	}
	// Binary must remain.
	if _, err := os.Stat(inst.BinPath()); err != nil {
		t.Errorf("binary must remain when abort triggered: %v", err)
	}
}

func TestUninstall_yesWithActiveService_forceServicesProceeds(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgentYAML(t, inst.StateDir, "watcher")

	unitsDir := t.TempDir()
	unitPath := filepath.Join(unitsDir, "watcher.plist")
	if err := os.WriteFile(unitPath, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubUnitFileMap(t, map[string]string{"watcher": unitPath})

	cmd := newUninstallCmdWith(inst)
	out, _, err := runCmd(t, cmd, "--yes", "--force-services")
	if err != nil {
		t.Fatalf("expected success with --force-services, got %v", err)
	}
	if !strings.Contains(out, "watcher") {
		t.Errorf("output should still cite the agent as a warning: %q", out)
	}
	if !strings.Contains(out, "uninstalled") {
		t.Errorf("expected uninstall to complete: %q", out)
	}
	if _, statErr := os.Stat(inst.BinPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("binary must be removed under --force-services")
	}
}

func TestUninstall_yesWithOnDemandAgent_proceedsWithoutForce(t *testing.T) {
	// Regression guard: agents WITHOUT an active service unit must NOT
	// trigger the abort, even when stateDir has agent.yaml entries.
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgentYAML(t, inst.StateDir, "smoke")

	// Resolver points at a non-existent file → no active service.
	stubUnitFileMap(t, map[string]string{
		"smoke": filepath.Join(t.TempDir(), "shipyard-crew-smoke.service"),
	})

	cmd := newUninstallCmdWith(inst)
	out, _, err := runCmd(t, cmd, "--yes")
	if err != nil {
		t.Fatalf("expected on-demand agent to proceed without --force-services, got %v", err)
	}
	if !strings.Contains(out, "smoke") {
		t.Errorf("expected on-demand agent to still be cited via warnIfAgentsRegistered: %q", out)
	}
	if strings.Contains(out, "active service unit") {
		t.Errorf("must NOT emit active-service warning for on-demand agent: %q", out)
	}
	if _, statErr := os.Stat(inst.BinPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("binary must be removed in the on-demand path")
	}
}

// ── Version ───────────────────────────────────────────────────────────────────

func TestVersion_text_installed(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\necho 'shipyard-crew 0.1.0 (test, built 2026-04-20)'\n")
	if err := os.WriteFile(inst.BinPath(), content, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newVersionCmdWith(inst, "1.0.9")
	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "shipyard      1.0.9") {
		t.Errorf("missing core line: %q", out)
	}
	if !strings.Contains(out, "shipyard-crew 0.1.0") {
		t.Errorf("missing addon line: %q", out)
	}
}

func TestVersion_text_notInstalled(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	cmd := newVersionCmdWith(inst, "1.0.9")
	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, VersionNotInstalled) {
		t.Errorf("expected %q, got %q", VersionNotInstalled, out)
	}
}

func TestVersion_json(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\necho 'shipyard-crew 0.1.0 (test, built 2026-04-20)'\n")
	if err := os.WriteFile(inst.BinPath(), content, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newVersionCmdWith(inst, "1.0.9")
	out, _, err := runCmd(t, cmd, "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := []string{`"shipyard":"1.0.9"`, `"shipyard_crew":"0.1.0"`, `"installed":true`}
	compact := strings.ReplaceAll(strings.ReplaceAll(out, " ", ""), "\n", "")
	for _, w := range want {
		if !strings.Contains(compact, w) {
			t.Errorf("json missing %q, got %q", w, compact)
		}
	}
}

// ── Exported constructors / builder ──────────────────────────────────────────

func TestNewInstallCmd_returnsConfiguredCommand(t *testing.T) {
	cmd := NewInstallCmd()
	if cmd.Use != "install" {
		t.Errorf("unexpected Use=%q", cmd.Use)
	}
	if cmd.Flag("force") == nil {
		t.Error("--force flag missing")
	}
	if cmd.Flag("version") == nil {
		t.Error("--version flag missing")
	}
}

func TestNewUninstallCmd_returnsConfiguredCommand(t *testing.T) {
	cmd := NewUninstallCmd()
	if cmd.Use != "uninstall" {
		t.Errorf("unexpected Use=%q", cmd.Use)
	}
	if cmd.Flag("yes") == nil {
		t.Error("--yes flag missing")
	}
}

func TestNewVersionCmd_returnsConfiguredCommand(t *testing.T) {
	cmd := NewVersionCmd()
	if cmd.Use != "version" {
		t.Errorf("unexpected Use=%q", cmd.Use)
	}
	if cmd.Flag("json") == nil {
		t.Error("--json flag missing")
	}
}

func TestInstall_unknownError_propagates(t *testing.T) {
	// Use an installer wired to an unreachable URL so the download fails with
	// a 404 — a generic error path (not ErrAlreadyInstalled/ErrUpgradeRequired).
	inst := newFakeInstaller(t, "0.1.0")
	inst.HTTPClient = &stubClient{} // no responses → every URL 404

	cmd := newInstallCmdWith(inst)
	_, _, err := runCmd(t, cmd)
	if err == nil {
		t.Fatal("expected error from failed download")
	}
}

func TestConfirmUninstall_emptyInput_declines(t *testing.T) {
	ok, err := confirmUninstall(strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Error("expected decline on empty input")
	}
}

func TestInstall_nilInstaller_usesBuilder(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	prev := crewInstallerBuilder
	crewInstallerBuilder = func(v string) (*crewctl.Installer, error) {
		inst.Version = v
		return inst, nil
	}
	t.Cleanup(func() { crewInstallerBuilder = prev })

	cmd := newInstallCmdWith(nil)
	out, _, err := runCmd(t, cmd, "--version", "0.1.0")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "installed successfully") {
		t.Errorf("missing success line: %q", out)
	}
}

func TestInstall_builderError_propagates(t *testing.T) {
	prev := crewInstallerBuilder
	crewInstallerBuilder = func(string) (*crewctl.Installer, error) {
		return nil, errors.New("builder boom")
	}
	t.Cleanup(func() { crewInstallerBuilder = prev })

	cmd := newInstallCmdWith(nil)
	_, _, err := runCmd(t, cmd)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected builder error, got %v", err)
	}
}

func TestUninstall_nilInstaller_usesBuilder(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := crewInstallerBuilder
	crewInstallerBuilder = func(string) (*crewctl.Installer, error) { return inst, nil }
	t.Cleanup(func() { crewInstallerBuilder = prev })

	cmd := newUninstallCmdWith(nil)
	_, _, err := runCmd(t, cmd, "--yes")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestVersion_buildsInstallerFromDefaults(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	prev := crewInstallerBuilder
	crewInstallerBuilder = func(string) (*crewctl.Installer, error) { return inst, nil }
	t.Cleanup(func() { crewInstallerBuilder = prev })

	cmd := newVersionCmdWith(nil, "1.0.9")
	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, VersionNotInstalled) {
		t.Errorf("expected not-installed marker, got %q", out)
	}
}

func TestBuildCrewInstaller_setsProductionDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst, err := buildCrewInstaller("0.1.0")
	if err != nil {
		t.Fatalf("buildCrewInstaller: %v", err)
	}
	if inst.Version != "0.1.0" {
		t.Errorf("version: got %q", inst.Version)
	}
	if inst.Platform.OS == "" || inst.Platform.Arch == "" {
		t.Error("platform not populated")
	}
	if !strings.HasSuffix(inst.BinDir, filepath.Join(".local", "bin")) {
		t.Errorf("BinDir: got %q", inst.BinDir)
	}
	if !strings.HasSuffix(inst.StateDir, filepath.Join(".shipyard", "crew")) {
		t.Errorf("StateDir: got %q", inst.StateDir)
	}
	if !strings.HasSuffix(inst.RunDir, filepath.Join(".shipyard", "run", "crew")) {
		t.Errorf("RunDir: got %q", inst.RunDir)
	}
	if inst.HTTPClient == nil {
		t.Error("HTTPClient nil")
	}
	if inst.ReleaseBase != crewctl.DefaultReleaseBase {
		t.Errorf("ReleaseBase: got %q", inst.ReleaseBase)
	}
}

func TestVersion_json_notInstalled(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	cmd := newVersionCmdWith(inst, "1.0.9")
	out, _, err := runCmd(t, cmd, "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, `"installed":false`) {
		t.Errorf("expected installed:false, got %q", out)
	}
	if !strings.Contains(out, `"functional":false`) {
		t.Errorf("expected functional:false, got %q", out)
	}
	if !strings.Contains(out, VersionNotInstalled) {
		t.Errorf("expected not-installed marker, got %q", out)
	}
}

// TestVersion_presentButNotFunctional_text covers C-08: a binary file
// exists at BinPath but cannot be exec'd (chmod 0644). Before the fix this
// collapsed to "(not installed)"; after, it must surface the divergence.
func TestVersion_presentButNotFunctional_text(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Plain non-executable file at BinPath.
	if err := os.WriteFile(inst.BinPath(), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newVersionCmdWith(inst, "1.0.9")
	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, VersionNotFunctional) {
		t.Errorf("expected %q in text output, got %q", VersionNotFunctional, out)
	}
	if strings.Contains(out, VersionNotInstalled) {
		t.Errorf("text output must NOT report not-installed for a present binary: %q", out)
	}
	if !strings.Contains(out, "error:") {
		t.Errorf("expected error: line surfacing exec failure, got %q", out)
	}
}

func TestVersion_presentButNotFunctional_json(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.BinPath(), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newVersionCmdWith(inst, "1.0.9")
	out, _, err := runCmd(t, cmd, "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	compact := strings.ReplaceAll(strings.ReplaceAll(out, " ", ""), "\n", "")
	for _, want := range []string{
		`"installed":true`,
		`"functional":false`,
		`"error":"`,
	} {
		if !strings.Contains(compact, want) {
			t.Errorf("json missing %q, got %q", want, compact)
		}
	}
}

func TestVersion_json_functionalWhenHealthy(t *testing.T) {
	inst := newFakeInstaller(t, "0.1.0")
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\necho 'shipyard-crew 0.1.0 (test, built 2026-04-20)'\n")
	if err := os.WriteFile(inst.BinPath(), content, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newVersionCmdWith(inst, "1.0.9")
	out, _, err := runCmd(t, cmd, "--json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	compact := strings.ReplaceAll(strings.ReplaceAll(out, " ", ""), "\n", "")
	if !strings.Contains(compact, `"installed":true`) {
		t.Errorf("expected installed:true, got %q", compact)
	}
	if !strings.Contains(compact, `"functional":true`) {
		t.Errorf("expected functional:true, got %q", compact)
	}
	if strings.Contains(compact, `"error":`) {
		t.Errorf("expected no error field when healthy, got %q", compact)
	}
}
