package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

func TestFlagEnv(t *testing.T) {
	cmd := newServiceAddCmd()
	if err := cmd.Flags().Set("env", "FOO=BAR"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("env", "API_URL=https://example.com"); err != nil {
		t.Fatal(err)
	}
	env := flagEnv(cmd, "env", []string{"FOO=BAR", "API_URL=https://example.com"})
	if env == nil || (*env)["FOO"] != "BAR" || (*env)["API_URL"] != "https://example.com" {
		t.Fatalf("unexpected env map: %+v", env)
	}
}

func TestHumanizeServiceError(t *testing.T) {
	err := humanizeServiceError(errors.New("plain"), "")
	if err == nil || err.Error() != "plain" {
		t.Fatalf("unexpected passthrough error: %v", err)
	}
}

func TestServiceConfigRequiresTTY(t *testing.T) {
	prev := tty.StdinFD
	tty.StdinFD = func() uintptr { return 0 }
	t.Cleanup(func() { tty.StdinFD = prev })

	cmd := newServiceConfigCmd()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	err := cmd.RunE(cmd, nil)
	if !errors.Is(err, tty.ErrNonInteractive) {
		t.Fatalf("expected tty.ErrNonInteractive, got %v", err)
	}
	if !strings.Contains(stderr.String(), "This command requires an interactive terminal.") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}
