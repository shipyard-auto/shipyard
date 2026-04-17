package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

func TestTTY_absent_exit2(t *testing.T) {
	cmd := newFairwayConfigCmdWith(fairwayConfigDeps{
		stdinFD:       func() uintptr { return 0 },
		stdoutFD:      func() uintptr { return 1 },
		isInteractive: func(uintptr) bool { return false },
	})
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	err := cmd.RunE(cmd, nil)
	if !errors.Is(err, tty.ErrNonInteractive) {
		t.Fatalf("expected tty.ErrNonInteractive, got %v", err)
	}
	if got := stderr.String(); got != "shipyard fairway config requires a terminal (TTY); use 'shipyard fairway route add' for non-interactive setup\n" {
		t.Fatalf("unexpected stderr: %q", got)
	}
}
