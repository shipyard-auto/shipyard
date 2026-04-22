package crew

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// expectedSubcommands is the canonical set of subcommands registered on the
// crew root command, in display order.
var expectedSubcommands = []string{
	"install", "uninstall", "version",
	"hire", "fire", "apply", "list", "run", "logs",
}

func TestNewCrewCmd_Help(t *testing.T) {
	cmd := NewCrewCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help execute: %v", err)
	}
	got := out.String()
	for _, sub := range expectedSubcommands {
		if !strings.Contains(got, sub) {
			t.Errorf("help output missing subcommand %q; got: %s", sub, got)
		}
	}
}

func TestNewCrewCmd_SubcommandsRegistered(t *testing.T) {
	cmd := NewCrewCmd()
	want := map[string]bool{}
	for _, name := range expectedSubcommands {
		want[name] = true
	}
	for _, sc := range cmd.Commands() {
		delete(want, sc.Name())
	}
	if len(want) > 0 {
		t.Errorf("missing subcommands: %v", want)
	}
}

func TestCrewRoot_NoArgs_PrintsHelp(t *testing.T) {
	cmd := NewCrewCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "shipyard crew") {
		t.Fatalf("expected help output, got: %s", out.String())
	}
}

// TestCrewRoot_GuardAppliedOnlyToBinaryCommands enforces the matrix:
// hire/fire/run carry PreRunE; install/uninstall/version/list/logs do not.
func TestCrewRoot_GuardAppliedOnlyToBinaryCommands(t *testing.T) {
	cmd := NewCrewCmd()
	needsGuard := map[string]bool{"hire": true, "fire": true, "apply": true, "run": true}
	for _, sc := range cmd.Commands() {
		if needsGuard[sc.Name()] {
			if sc.PreRunE == nil {
				t.Errorf("%s must have PreRunE", sc.Name())
			}
			continue
		}
		if sc.PreRunE != nil {
			t.Errorf("%s must NOT have PreRunE", sc.Name())
		}
	}
}

// TestCrewRoot_RunGuardTriggersWhenBinaryMissing executes the full root
// command path `shipyard crew run demo` with a resolver that fails; the
// guard must short-circuit before run.go logic fires.
func TestCrewRoot_RunGuardTriggersWhenBinaryMissing(t *testing.T) {
	prev := resolveBinaryFn
	resolveBinaryFn = func() (string, error) { return "", errors.New("gone") }
	t.Cleanup(func() { resolveBinaryFn = prev })

	cmd := NewCrewCmd()
	cmd.SetArgs([]string{"run", "demo"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected guard error")
	}
	if !errors.Is(err, ErrAddonNotInstalled) {
		t.Fatalf("want ErrAddonNotInstalled, got %v", err)
	}
	if !strings.Contains(err.Error(), "shipyard crew install") {
		t.Fatalf("error must mention install command: %v", err)
	}
}
