package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/commsctl"
)

// echoCmdHelper is exec'd by the test binary itself: when we call
// `os.Args[0] -test.run=TestHelperProcess --` with extra args, Go re-runs
// this test binary and TestHelperProcess takes over the execution as if
// it were the real shipyard-comms binary. Standard Go subprocess-mocking
// pattern; see also internal/crewctl tests.
func echoCmdHelper(t *testing.T, deps commsPassthroughDeps, scenario string) commsPassthroughDeps {
	t.Helper()
	deps.ResolveBinary = func() (string, error) { return os.Args[0], nil }
	deps.MakeCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=TestHelperProcess", "--", scenario}, args...)
		c := exec.CommandContext(ctx, name, cs...)
		c.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return c
	}
	return deps
}

// TestHelperProcess is the helper executed in a subprocess; it is NOT a
// real test from Go's perspective when GO_WANT_HELPER_PROCESS is unset.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	// Args after "--": [scenario, <forwarded args from passthrough>].
	args := os.Args
	idx := -1
	for i, a := range args {
		if a == "--" {
			idx = i + 1
			break
		}
	}
	if idx < 0 || idx >= len(args) {
		os.Exit(2)
	}
	scenario := args[idx]
	forwarded := args[idx+1:]

	switch scenario {
	case "echo-args":
		// Echo the forwarded args to stdout, comma-separated, then exit 0.
		_, _ = io.WriteString(os.Stdout, strings.Join(forwarded, ","))
		os.Exit(0)
	case "echo-stdin":
		// Copy stdin → stdout, exit 0.
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	case "fail":
		_, _ = io.WriteString(os.Stderr, "comms: simulated failure\n")
		os.Exit(7)
	default:
		os.Exit(99)
	}
}

func TestCommsChannel_ForwardsArgsWithChannelPrefix(t *testing.T) {
	var stdout bytes.Buffer
	deps := commsPassthroughDeps{Stdout: &stdout, Stderr: &bytes.Buffer{}}
	deps = echoCmdHelper(t, deps, "echo-args")

	cmd := newCommsChannelCmdWith(deps)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"list", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := stdout.String(); got != "channel,list,--json" {
		t.Errorf("forwarded args = %q, want channel,list,--json", got)
	}
}

func TestCommsSend_ForwardsArgsWithSendPrefix(t *testing.T) {
	var stdout bytes.Buffer
	deps := commsPassthroughDeps{Stdout: &stdout, Stderr: &bytes.Buffer{}}
	deps = echoCmdHelper(t, deps, "echo-args")

	cmd := newCommsSendCmdWith(deps)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--channel", "tg", "--to", "12345", "--text", "hi"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := stdout.String(); got != "send,--channel,tg,--to,12345,--text,hi" {
		t.Errorf("forwarded args = %q", got)
	}
}

func TestCommsSend_StreamsStdinThrough(t *testing.T) {
	var stdout bytes.Buffer
	deps := commsPassthroughDeps{
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		Stdin:  strings.NewReader("piped body"),
	}
	deps = echoCmdHelper(t, deps, "echo-stdin")

	cmd := newCommsSendCmdWith(deps)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--stdin-body"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := stdout.String(); got != "piped body" {
		t.Errorf("stdin not streamed through: got %q", got)
	}
}

func TestCommsPassthrough_NotInstalled(t *testing.T) {
	deps := commsPassthroughDeps{
		ResolveBinary: func() (string, error) { return "", commsctl.ErrNotInstalled },
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
	}
	cmd := newCommsSendCmdWith(deps)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--channel", "x"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected not-installed error")
	}
	if !strings.Contains(err.Error(), "shipyard comms install") {
		t.Errorf("error should point at install command, got %v", err)
	}
}

func TestCommsPassthrough_ResolveBinaryError(t *testing.T) {
	deps := commsPassthroughDeps{
		ResolveBinary: func() (string, error) { return "", errors.New("disk gone") },
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
	}
	cmd := newCommsSendCmdWith(deps)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--channel", "x"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "disk gone") {
		t.Errorf("expected wrapped resolve error, got %v", err)
	}
}

func TestCommsPassthrough_PropagatesExitCode(t *testing.T) {
	deps := commsPassthroughDeps{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	deps = echoCmdHelper(t, deps, "fail")

	cmd := newCommsSendCmdWith(deps)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--channel", "x"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from exit-7 subprocess")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *exec.ExitError, got %T", err)
	}
	if got := ee.ExitCode(); got != 7 {
		t.Errorf("exit code = %d, want 7", got)
	}
}
