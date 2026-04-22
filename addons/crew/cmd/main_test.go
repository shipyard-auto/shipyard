package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/app"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/daemon"
)

const fakeAgentYAML = `schema_version: "1"
name: %s
description: test
backend:
  type: cli
  command: ["/bin/echo"]
execution:
  mode: service
  pool: cli
conversation:
  mode: stateless
triggers:
  - type: cron
    schedule: "0 * * * *"
tools: []
`

func writeAgent(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, "crew", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(fmt.Sprintf(fakeAgentYAML, name)), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("prompt"), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
}

func canceledSignalCtx(_ context.Context) (context.Context, context.CancelFunc) {
	c, cancel := context.WithCancel(context.Background())
	cancel()
	return c, func() {}
}

func fakeService(code int, err error) func(context.Context, daemon.Options) (int, error) {
	return func(context.Context, daemon.Options) (int, error) { return code, err }
}

func fakeOnDemand(code int, err error, stdoutLine string) func(context.Context, onDemandRequest) (int, error) {
	return func(_ context.Context, req onDemandRequest) (int, error) {
		if stdoutLine != "" && req.Stdout != nil {
			fmt.Fprintln(req.Stdout, stdoutLine)
		}
		return code, err
	}
}

func newDeps(args []string, env map[string]string) (runtimeDeps, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return runtimeDeps{
		Args:        args,
		Env:         func(k string) string { return env[k] },
		Stdin:       strings.NewReader(""),
		Stdout:      stdout,
		Stderr:      stderr,
		Now:         func() time.Time { return time.Unix(0, 0) },
		Exit:        func(int) {},
		SignalCtx:   canceledSignalCtx,
		RunService:  fakeService(ExitOK, nil),
		RunOnDemand: fakeOnDemand(ExitOK, nil, `{"status":"ok"}`),
	}, stdout, stderr
}

func TestRunFlagParsing(t *testing.T) {
	longName := strings.Repeat("a", 64)

	cases := []struct {
		name       string
		args       []string
		wantExit   int
		wantStdout string
		wantStderr string
	}{
		{name: "version flag", args: []string{"--version"}, wantExit: ExitOK, wantStdout: "shipyard-crew dev"},
		{name: "help flag", args: []string{"-h"}, wantExit: ExitOK, wantStdout: "Usage of"},
		{name: "agent missing", args: []string{}, wantExit: ExitInvalidInput, wantStderr: "invalid --agent"},
		{name: "agent uppercase", args: []string{"--agent", "Bad"}, wantExit: ExitInvalidInput, wantStderr: "invalid --agent"},
		{name: "agent empty", args: []string{"--agent", ""}, wantExit: ExitInvalidInput, wantStderr: "invalid --agent"},
		{name: "agent too long", args: []string{"--agent", longName}, wantExit: ExitInvalidInput, wantStderr: "invalid --agent"},
		{name: "agent leading dash", args: []string{"--agent", "-bad"}, wantExit: ExitInvalidInput, wantStderr: "invalid --agent"},
		{name: "unknown flag", args: []string{"--wat"}, wantExit: ExitInvalidInput, wantStderr: "flag provided but not defined"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, stdout, stderr := newDeps(tc.args, nil)
			got := run(context.Background(), deps)
			if got != tc.wantExit {
				t.Fatalf("exit = %d, want %d\nstdout=%q\nstderr=%q", got, tc.wantExit, stdout.String(), stderr.String())
			}
			if tc.wantStdout != "" && !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Errorf("stdout missing %q; got %q", tc.wantStdout, stdout.String())
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr missing %q; got %q", tc.wantStderr, stderr.String())
			}
		})
	}
}

func TestRunVersionCustomInjection(t *testing.T) {
	origV, origC, origB := app.Version, app.Commit, app.BuildDate
	t.Cleanup(func() { app.Version, app.Commit, app.BuildDate = origV, origC, origB })
	app.Version = "9.9.9"
	app.Commit = "deadbee"
	app.BuildDate = "2026-04-20"

	deps, stdout, stderr := newDeps([]string{"--version"}, nil)
	got := run(context.Background(), deps)
	if got != ExitOK {
		t.Fatalf("exit = %d, want 0", got)
	}
	want := "shipyard-crew 9.9.9 (deadbee, built 2026-04-20)\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunOnDemandPathResolution(t *testing.T) {
	// Capture the paths the main binary routes into on-demand mode.
	var captured onDemandRequest
	deps, _, _ := newDeps([]string{"--agent", "alpha"}, map[string]string{"SHIPYARD_HOME": "/tmp/sy-home"})
	deps.RunOnDemand = func(_ context.Context, req onDemandRequest) (int, error) {
		captured = req
		return ExitOK, nil
	}
	if got := run(context.Background(), deps); got != ExitOK {
		t.Fatalf("exit = %d", got)
	}
	if captured.AgentName != "alpha" {
		t.Errorf("AgentName = %q", captured.AgentName)
	}
	wantDir := filepath.Join("/tmp/sy-home", "crew", "alpha")
	if captured.AgentDir != wantDir {
		t.Errorf("AgentDir = %q, want %q", captured.AgentDir, wantDir)
	}
	wantCfg := filepath.Join("/tmp/sy-home", "crew", "config.yaml")
	if captured.ConfigPath != wantCfg {
		t.Errorf("ConfigPath = %q, want %q", captured.ConfigPath, wantCfg)
	}
}

func TestRunServicePathResolution(t *testing.T) {
	var captured daemon.Options
	deps, _, _ := newDeps([]string{"--agent", "alpha", "--service"}, map[string]string{"SHIPYARD_HOME": "/tmp/sy"})
	deps.RunService = func(_ context.Context, opts daemon.Options) (int, error) {
		captured = opts
		return ExitOK, nil
	}
	if got := run(context.Background(), deps); got != ExitOK {
		t.Fatalf("exit = %d", got)
	}
	if captured.AgentName != "alpha" {
		t.Errorf("AgentName = %q", captured.AgentName)
	}
	if captured.AgentDir != "/tmp/sy/crew/alpha" {
		t.Errorf("AgentDir = %q", captured.AgentDir)
	}
	if captured.RunDir != "/tmp/sy/run/crew" {
		t.Errorf("RunDir = %q", captured.RunDir)
	}
}

func TestRunOnDemandInvalidStdin(t *testing.T) {
	deps, _, stderr := newDeps([]string{"--agent", "x"}, nil)
	deps.Stdin = strings.NewReader("{ not json")
	got := run(context.Background(), deps)
	if got != ExitInvalidInput {
		t.Fatalf("exit = %d, want %d", got, ExitInvalidInput)
	}
	if !strings.Contains(stderr.String(), "invalid stdin json") {
		t.Errorf("stderr missing json error: %q", stderr.String())
	}
}

func TestRunOnDemandStdinTooLarge(t *testing.T) {
	big := strings.Repeat("a", maxOnDemandStdin+1)
	deps, _, stderr := newDeps([]string{"--agent", "x"}, nil)
	deps.Stdin = strings.NewReader(big)
	got := run(context.Background(), deps)
	if got != ExitInvalidInput {
		t.Fatalf("exit = %d, want %d", got, ExitInvalidInput)
	}
	if !strings.Contains(stderr.String(), "exceeds") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunOnDemandPassesInput(t *testing.T) {
	var captured map[string]any
	deps, _, _ := newDeps([]string{"--agent", "x"}, nil)
	deps.Stdin = strings.NewReader(`{"user":"hi","n":2}`)
	deps.RunOnDemand = func(_ context.Context, req onDemandRequest) (int, error) {
		captured = req.Input
		return ExitOK, nil
	}
	if got := run(context.Background(), deps); got != ExitOK {
		t.Fatalf("exit = %d", got)
	}
	if captured["user"] != "hi" {
		t.Errorf("user = %v", captured["user"])
	}
	if captured["n"].(float64) != 2 {
		t.Errorf("n = %v", captured["n"])
	}
}

func TestRunOnDemandPropagatesExit(t *testing.T) {
	cases := []int{ExitBusinessFailure, ExitOnDemandInternal, ExitInvalidConfig, ExitBuildRuntime}
	for _, want := range cases {
		t.Run(fmt.Sprintf("exit_%d", want), func(t *testing.T) {
			deps, _, _ := newDeps([]string{"--agent", "x"}, nil)
			deps.RunOnDemand = fakeOnDemand(want, nil, "")
			if got := run(context.Background(), deps); got != want {
				t.Errorf("exit = %d, want %d", got, want)
			}
		})
	}
}

func TestRunServicePropagatesExit(t *testing.T) {
	cases := []struct {
		name string
		code int
		err  error
	}{
		{"already running", ExitAlreadyRunning, errors.New("pid busy")},
		{"invalid config", ExitInvalidConfig, errors.New("bad yaml")},
		{"build runtime", ExitBuildRuntime, errors.New("build fail")},
		{"shutdown timeout", ExitShutdownTimeout, errors.New("timed out")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, stderr := newDeps([]string{"--agent", "x", "--service"}, nil)
			deps.RunService = fakeService(tc.code, tc.err)
			if got := run(context.Background(), deps); got != tc.code {
				t.Errorf("exit = %d, want %d", got, tc.code)
			}
			if !strings.Contains(stderr.String(), tc.err.Error()) {
				t.Errorf("stderr missing %q: %q", tc.err.Error(), stderr.String())
			}
		})
	}
}

func TestExitCodeContract(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"ExitOK", ExitOK, 0},
		{"ExitBusinessFailure", ExitBusinessFailure, 1},
		{"ExitInvalidInput", ExitInvalidInput, 2},
		{"ExitOnDemandInternal", ExitOnDemandInternal, 3},
		{"ExitAlreadyRunning", ExitAlreadyRunning, 10},
		{"ExitInvalidConfig", ExitInvalidConfig, 20},
		{"ExitBuildRuntime", ExitBuildRuntime, 30},
		{"ExitShutdownTimeout", ExitShutdownTimeout, 50},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

func TestAgentNameRegex(t *testing.T) {
	ok := []string{"a", "0", "abc", "promo-hunter", "promo_hunter", "x1", "a" + strings.Repeat("b", 62)}
	bad := []string{"", "A", "-bad", "_bad", "bad!", "bad/name", strings.Repeat("a", 64)}
	for _, s := range ok {
		if !agentNameRe.MatchString(s) {
			t.Errorf("want %q to match", s)
		}
	}
	for _, s := range bad {
		if agentNameRe.MatchString(s) {
			t.Errorf("want %q to NOT match", s)
		}
	}
}

func TestDefaultRunOnDemandAgentNotFound(t *testing.T) {
	home := t.TempDir()
	code, err := defaultRunOnDemand(context.Background(), onDemandRequest{
		AgentName:  "ghost",
		AgentDir:   filepath.Join(home, "does-not-exist"),
		ConfigPath: "",
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if code != ExitInvalidConfig {
		t.Errorf("code = %d, want %d", code, ExitInvalidConfig)
	}
	if err == nil {
		t.Errorf("expected error")
	}
}

func TestDefaultRunOnDemandAgentMismatch(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "alpha")
	code, err := defaultRunOnDemand(context.Background(), onDemandRequest{
		AgentName:  "beta",
		AgentDir:   filepath.Join(home, "crew", "alpha"),
		ConfigPath: "",
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if code != ExitInvalidConfig {
		t.Errorf("code = %d, want %d", code, ExitInvalidConfig)
	}
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultRunOnDemandSuccess(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "omega")
	stdout := &bytes.Buffer{}
	code, err := defaultRunOnDemand(context.Background(), onDemandRequest{
		AgentName:  "omega",
		AgentDir:   filepath.Join(home, "crew", "omega"),
		ConfigPath: "",
		Input:      map[string]any{"user": "hi"},
		Stdout:     stdout,
		Stderr:     &bytes.Buffer{},
	})
	if code != ExitOK {
		t.Fatalf("code = %d, err=%v", code, err)
	}
	var env map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("envelope not json: %v; raw=%q", err, stdout.String())
	}
	if env["status"] != "ok" {
		t.Errorf("status = %v", env["status"])
	}
	if env["trace_id"] == "" {
		t.Errorf("trace_id empty")
	}
}

func TestWithDefaultsPopulatesAll(t *testing.T) {
	d := runtimeDeps{}.withDefaults()
	if d.Env == nil || d.Stdout == nil || d.Stderr == nil || d.Stdin == nil {
		t.Errorf("default io not set")
	}
	if d.Now == nil || d.Exit == nil || d.SignalCtx == nil {
		t.Errorf("default fns not set")
	}
	if d.RunService == nil || d.RunOnDemand == nil {
		t.Errorf("default runners not set")
	}
}

func TestReadStdinLimit(t *testing.T) {
	b, err := readStdinLimit(nil, 10)
	if err != nil || b != nil {
		t.Errorf("nil reader: b=%v err=%v", b, err)
	}
	b, err = readStdinLimit(strings.NewReader("hello"), 10)
	if err != nil || string(b) != "hello" {
		t.Errorf("short: b=%q err=%v", b, err)
	}
	_, err = readStdinLimit(strings.NewReader("12345678901"), 10)
	if err == nil {
		t.Errorf("expected exceed error")
	}
}

func TestWriteEnvelope(t *testing.T) {
	buf := &bytes.Buffer{}
	if err := writeEnvelope(buf, map[string]any{"status": "ok", "trace_id": "abc"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["status"] != "ok" {
		t.Errorf("status = %v", decoded["status"])
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("envelope should end with newline: %q", buf.String())
	}
}
