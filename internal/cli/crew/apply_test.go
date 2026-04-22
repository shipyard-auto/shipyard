package crew

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeApplyAgentDir(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, "crew", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("name: "+name+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func applyDepsFor(t *testing.T, home string) (applyDeps, *bytes.Buffer, *bytes.Buffer, *[][]string) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	var commands [][]string
	return applyDeps{
		Home:        home,
		Stdout:      stdout,
		Stderr:      stderr,
		LookPath:    func(string) (string, error) { return "/usr/bin/shipyard-crew", nil },
		MakeCommand: fakeCommand(&commands, 0, "ok\n", ""),
	}, stdout, stderr, &commands
}

func TestRunApplyInvalidName(t *testing.T) {
	home := t.TempDir()
	deps, _, stderr, _ := applyDepsFor(t, home)
	code := runApply(context.Background(), deps, "Bad!Name", applyFlags{})
	if code != applyExitNotFound {
		t.Errorf("code = %d, want %d", code, applyExitNotFound)
	}
	if !strings.Contains(stderr.String(), "invalid name") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunApplyAgentDirMissing(t *testing.T) {
	home := t.TempDir()
	deps, _, stderr, _ := applyDepsFor(t, home)
	code := runApply(context.Background(), deps, "ghost", applyFlags{})
	if code != applyExitNotFound {
		t.Errorf("code = %d, want %d", code, applyExitNotFound)
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunApplyAgentDirIsFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "crew"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "crew", "notadir"), []byte{}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	deps, _, stderr, _ := applyDepsFor(t, home)
	code := runApply(context.Background(), deps, "notadir", applyFlags{})
	if code != applyExitNotFound {
		t.Errorf("code = %d, want %d", code, applyExitNotFound)
	}
	if !strings.Contains(stderr.String(), "not a directory") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunApplyBinaryMissing(t *testing.T) {
	home := t.TempDir()
	writeApplyAgentDir(t, home, "alpha")
	deps, _, stderr, _ := applyDepsFor(t, home)
	deps.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	code := runApply(context.Background(), deps, "alpha", applyFlags{})
	if code != applyExitError {
		t.Errorf("code = %d, want %d", code, applyExitError)
	}
	if !strings.Contains(stderr.String(), "not found in PATH") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunApplyHappyPath(t *testing.T) {
	home := t.TempDir()
	writeApplyAgentDir(t, home, "alpha")
	deps, stdout, _, commands := applyDepsFor(t, home)
	code := runApply(context.Background(), deps, "alpha", applyFlags{})
	if code != applyExitOK {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if len(*commands) != 1 {
		t.Fatalf("expected 1 subprocess, got %d", len(*commands))
	}
	got := (*commands)[0]
	// fakeCommand wraps everything into `sh -c <script>`; assert the recorded
	// args (the *original* args we asked for) hold the right flags.
	joined := strings.Join(got, " ")
	for _, want := range []string{"reconcile", "--agent", "alpha"} {
		if !strings.Contains(joined, want) {
			t.Errorf("subprocess args missing %q: %v", want, got)
		}
	}
	for _, bad := range []string{"--dry-run", "--json"} {
		if strings.Contains(joined, bad) {
			t.Errorf("subprocess args should not contain %q: %v", bad, got)
		}
	}
}

func TestRunApplyPropagatesDryRunAndJSON(t *testing.T) {
	home := t.TempDir()
	writeApplyAgentDir(t, home, "alpha")
	deps, _, _, commands := applyDepsFor(t, home)
	code := runApply(context.Background(), deps, "alpha", applyFlags{DryRun: true, JSON: true})
	if code != applyExitOK {
		t.Fatalf("code = %d", code)
	}
	if len(*commands) != 1 {
		t.Fatalf("expected 1 subprocess, got %d", len(*commands))
	}
	joined := strings.Join((*commands)[0], " ")
	for _, want := range []string{"reconcile", "--agent", "alpha", "--dry-run", "--json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("subprocess args missing %q: %v", want, (*commands)[0])
		}
	}
}

func TestRunApplyPropagatesExitCodes(t *testing.T) {
	cases := []int{0, 1, 2}
	for _, want := range cases {
		t.Run(fmt.Sprintf("exit_%d", want), func(t *testing.T) {
			home := t.TempDir()
			writeApplyAgentDir(t, home, "alpha")
			var commands [][]string
			deps := applyDeps{
				Home:        home,
				Stdout:      &bytes.Buffer{},
				Stderr:      &bytes.Buffer{},
				LookPath:    func(string) (string, error) { return "/usr/bin/shipyard-crew", nil },
				MakeCommand: fakeCommand(&commands, want, "", ""),
			}
			got := runApply(context.Background(), deps, "alpha", applyFlags{})
			if got != want {
				t.Errorf("got = %d, want %d", got, want)
			}
		})
	}
}

func TestRunApplyPropagatesStdoutStderr(t *testing.T) {
	home := t.TempDir()
	writeApplyAgentDir(t, home, "alpha")
	var commands [][]string
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	deps := applyDeps{
		Home:        home,
		Stdout:      stdout,
		Stderr:      stderr,
		LookPath:    func(string) (string, error) { return "/usr/bin/shipyard-crew", nil },
		MakeCommand: fakeCommand(&commands, 2, "envelope-out", "scary-err"),
	}
	code := runApply(context.Background(), deps, "alpha", applyFlags{})
	if code != 2 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout.String(), "envelope-out") {
		t.Errorf("stdout should forward subprocess stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "scary-err") {
		t.Errorf("stderr should forward subprocess stderr: %q", stderr.String())
	}
}

func TestRunApplySubprocessStartFailure(t *testing.T) {
	home := t.TempDir()
	writeApplyAgentDir(t, home, "alpha")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	deps := applyDeps{
		Home:     home,
		Stdout:   stdout,
		Stderr:   stderr,
		LookPath: func(string) (string, error) { return "/nonexistent/shipyard-crew", nil },
		MakeCommand: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			// Produce a command that fails to start (ExecPath is empty).
			return exec.CommandContext(ctx, "/nonexistent/shipyard-crew", args...)
		},
	}
	code := runApply(context.Background(), deps, "alpha", applyFlags{})
	if code != applyExitError {
		t.Errorf("code = %d, want %d", code, applyExitError)
	}
	if !strings.Contains(stderr.String(), "subprocess failed") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestNewApplyCmdWiresFlags(t *testing.T) {
	home := t.TempDir()
	writeApplyAgentDir(t, home, "alpha")
	var commands [][]string
	deps := applyDeps{
		Home:        home,
		LookPath:    func(string) (string, error) { return "/usr/bin/shipyard-crew", nil },
		MakeCommand: fakeCommand(&commands, 0, "", ""),
	}
	cmd := newApplyCmdWith(deps)
	cmd.SetArgs([]string{"alpha", "--dry-run", "--json"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	// Skip PreRunE (requireInstalled) which checks PATH for the binary.
	cmd.PreRunE = nil
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 subprocess, got %d", len(commands))
	}
	joined := strings.Join(commands[0], " ")
	for _, want := range []string{"--dry-run", "--json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("flags lost: args=%v", commands[0])
		}
	}
}

func TestApplyWithDefaults(t *testing.T) {
	d := applyDeps{}.withDefaults()
	if d.Stdout == nil || d.Stderr == nil {
		t.Errorf("default io not set")
	}
	if d.LookPath == nil || d.MakeCommand == nil {
		t.Errorf("default subprocess hooks not set")
	}
}

func TestApplyExitCodeContract(t *testing.T) {
	if applyExitOK != 0 {
		t.Errorf("applyExitOK = %d, want 0", applyExitOK)
	}
	if applyExitNotFound != 1 {
		t.Errorf("applyExitNotFound = %d, want 1", applyExitNotFound)
	}
	if applyExitError != 2 {
		t.Errorf("applyExitError = %d, want 2", applyExitError)
	}
}
