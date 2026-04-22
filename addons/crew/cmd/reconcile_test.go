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

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/trigger"
)

func fakeReconcile(code int, err error, stdoutLine string) func(context.Context, reconcileRequest) (int, error) {
	return func(_ context.Context, req reconcileRequest) (int, error) {
		if stdoutLine != "" && req.Stdout != nil {
			fmt.Fprintln(req.Stdout, stdoutLine)
		}
		return code, err
	}
}

func TestReconcileFlagParsing(t *testing.T) {
	longName := strings.Repeat("a", 64)

	cases := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{name: "agent missing", args: []string{"reconcile"}, wantExit: ExitReconcileError, wantStderr: "invalid --agent"},
		{name: "agent uppercase", args: []string{"reconcile", "--agent", "Bad"}, wantExit: ExitReconcileError, wantStderr: "invalid --agent"},
		{name: "agent empty", args: []string{"reconcile", "--agent", ""}, wantExit: ExitReconcileError, wantStderr: "invalid --agent"},
		{name: "agent too long", args: []string{"reconcile", "--agent", longName}, wantExit: ExitReconcileError, wantStderr: "invalid --agent"},
		{name: "unknown flag", args: []string{"reconcile", "--wat"}, wantExit: ExitReconcileError, wantStderr: "flag provided but not defined"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, stdout, stderr := newDeps(tc.args, nil)
			deps.RunReconcile = fakeReconcile(ExitReconcileOK, nil, "")
			got := run(context.Background(), deps)
			if got != tc.wantExit {
				t.Fatalf("exit = %d, want %d\nstdout=%q\nstderr=%q", got, tc.wantExit, stdout.String(), stderr.String())
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr missing %q; got %q", tc.wantStderr, stderr.String())
			}
		})
	}
}

func TestReconcilePathResolution(t *testing.T) {
	var captured reconcileRequest
	deps, _, _ := newDeps([]string{"reconcile", "--agent", "alpha", "--dry-run", "--json"}, map[string]string{"SHIPYARD_HOME": "/tmp/sy-home"})
	deps.RunReconcile = func(_ context.Context, req reconcileRequest) (int, error) {
		captured = req
		return ExitReconcileOK, nil
	}
	if got := run(context.Background(), deps); got != ExitReconcileOK {
		t.Fatalf("exit = %d", got)
	}
	if captured.AgentName != "alpha" {
		t.Errorf("AgentName = %q", captured.AgentName)
	}
	wantDir := filepath.Join("/tmp/sy-home", "crew", "alpha")
	if captured.AgentDir != wantDir {
		t.Errorf("AgentDir = %q, want %q", captured.AgentDir, wantDir)
	}
	if captured.Home != "/tmp/sy-home" {
		t.Errorf("Home = %q", captured.Home)
	}
	if !captured.DryRun {
		t.Errorf("DryRun not propagated")
	}
	if !captured.JSON {
		t.Errorf("JSON not propagated")
	}
}

func TestReconcileDefaultFlags(t *testing.T) {
	var captured reconcileRequest
	deps, _, _ := newDeps([]string{"reconcile", "--agent", "alpha"}, map[string]string{"SHIPYARD_HOME": "/tmp/sy"})
	deps.RunReconcile = func(_ context.Context, req reconcileRequest) (int, error) {
		captured = req
		return ExitReconcileOK, nil
	}
	if got := run(context.Background(), deps); got != ExitReconcileOK {
		t.Fatalf("exit = %d", got)
	}
	if captured.DryRun {
		t.Errorf("DryRun should default to false")
	}
	if captured.JSON {
		t.Errorf("JSON should default to false")
	}
}

func TestReconcilePropagatesExit(t *testing.T) {
	cases := []int{ExitReconcileOK, ExitReconcileNotFound, ExitReconcileError}
	for _, want := range cases {
		t.Run(fmt.Sprintf("exit_%d", want), func(t *testing.T) {
			deps, _, _ := newDeps([]string{"reconcile", "--agent", "x"}, nil)
			deps.RunReconcile = fakeReconcile(want, nil, "")
			if got := run(context.Background(), deps); got != want {
				t.Errorf("exit = %d, want %d", got, want)
			}
		})
	}
}

func TestReconcilePropagatesError(t *testing.T) {
	deps, _, stderr := newDeps([]string{"reconcile", "--agent", "x"}, nil)
	deps.RunReconcile = fakeReconcile(ExitReconcileError, errors.New("cron reconcile: boom"), "")
	if got := run(context.Background(), deps); got != ExitReconcileError {
		t.Fatalf("exit = %d", got)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("stderr missing error: %q", stderr.String())
	}
}

func TestReconcileExitCodeContract(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"ExitReconcileOK", ExitReconcileOK, 0},
		{"ExitReconcileNotFound", ExitReconcileNotFound, 1},
		{"ExitReconcileError", ExitReconcileError, 2},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

func TestReconcileUsesHomeDirFallbackWhenEnvEmpty(t *testing.T) {
	var captured reconcileRequest
	deps, _, _ := newDeps([]string{"reconcile", "--agent", "alpha"}, nil)
	deps.RunReconcile = func(_ context.Context, req reconcileRequest) (int, error) {
		captured = req
		return ExitReconcileOK, nil
	}
	if got := run(context.Background(), deps); got != ExitReconcileOK {
		t.Fatalf("exit = %d", got)
	}
	if captured.Home == "" {
		t.Errorf("Home should fall back to <user>/.shipyard, got empty")
	}
	if !strings.HasSuffix(captured.Home, ".shipyard") {
		t.Errorf("Home = %q, expected default ending with .shipyard", captured.Home)
	}
}

func TestDefaultRunReconcileAgentDirMissing(t *testing.T) {
	home := t.TempDir()
	code, err := defaultRunReconcile(context.Background(), reconcileRequest{
		AgentName: "ghost",
		AgentDir:  filepath.Join(home, "crew", "ghost"),
		Home:      home,
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
	})
	if code != ExitReconcileNotFound {
		t.Errorf("code = %d, want %d", code, ExitReconcileNotFound)
	}
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultRunReconcileAgentNameMismatch(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "alpha")
	code, err := defaultRunReconcile(context.Background(), reconcileRequest{
		AgentName: "beta",
		AgentDir:  filepath.Join(home, "crew", "alpha"),
		Home:      home,
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
	})
	if code != ExitReconcileNotFound {
		t.Errorf("code = %d, want %d", code, ExitReconcileNotFound)
	}
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultRunReconcileAgentYamlInvalid(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "crew", "alpha")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("not: valid: yaml: ["), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	code, err := defaultRunReconcile(context.Background(), reconcileRequest{
		AgentName: "alpha",
		AgentDir:  dir,
		Home:      home,
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
	})
	if code != ExitReconcileNotFound {
		t.Errorf("code = %d, want %d", code, ExitReconcileNotFound)
	}
	if err == nil {
		t.Errorf("expected error")
	}
}

func TestWriteReconcileResultJSON(t *testing.T) {
	buf := &bytes.Buffer{}
	env := reconcileEnvelope{
		Agent:  "alpha",
		DryRun: true,
		Cron: trigger.CronDiff{
			Added:     []trigger.CronChange{{Name: "crew:alpha:0", Schedule: "* * * * *"}},
			Removed:   []trigger.CronChange{},
			Unchanged: []trigger.CronChange{},
		},
		Webhooks: trigger.WebhookDiff{
			Added:     []trigger.WebhookChange{},
			Removed:   []trigger.WebhookChange{},
			Unchanged: []trigger.WebhookChange{{Route: "/hooks/alpha"}},
		},
	}
	if err := writeReconcileResult(buf, env, true); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("output should end with newline: %q", buf.String())
	}
	var decoded reconcileEnvelope
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v; raw=%q", err, buf.String())
	}
	if decoded.Agent != "alpha" {
		t.Errorf("agent = %q", decoded.Agent)
	}
	if !decoded.DryRun {
		t.Errorf("dry_run lost in round-trip")
	}
	if len(decoded.Cron.Added) != 1 || decoded.Cron.Added[0].Name != "crew:alpha:0" {
		t.Errorf("cron.added = %+v", decoded.Cron.Added)
	}
	if len(decoded.Webhooks.Unchanged) != 1 || decoded.Webhooks.Unchanged[0].Route != "/hooks/alpha" {
		t.Errorf("webhooks.unchanged = %+v", decoded.Webhooks.Unchanged)
	}
}

func TestWriteReconcileResultJSONOmitsDryRunWhenFalse(t *testing.T) {
	buf := &bytes.Buffer{}
	env := reconcileEnvelope{Agent: "alpha"}
	if err := writeReconcileResult(buf, env, true); err != nil {
		t.Fatalf("write: %v", err)
	}
	if strings.Contains(buf.String(), "dry_run") {
		t.Errorf("dry_run should be omitted when false: %q", buf.String())
	}
}

func TestWriteReconcileResultHumanReconciled(t *testing.T) {
	buf := &bytes.Buffer{}
	env := reconcileEnvelope{
		Agent: "alpha",
		Cron: trigger.CronDiff{
			Added: []trigger.CronChange{
				{Name: "crew:alpha:0", Schedule: "* * * * *", ID: "cron-xyz"},
			},
			Removed: []trigger.CronChange{
				{Name: "crew:alpha:1", ID: "cron-old"},
			},
			Unchanged: []trigger.CronChange{},
		},
		Webhooks: trigger.WebhookDiff{
			Added:   []trigger.WebhookChange{{Route: "/hooks/a"}},
			Removed: []trigger.WebhookChange{{Route: "/hooks/b"}},
		},
	}
	if err := writeReconcileResult(buf, env, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "Reconciled \"alpha\":") {
		t.Errorf("missing Reconciled header: %q", out)
	}
	for _, want := range []string{
		"cron:    +1 -1 =0",
		"+ crew:alpha:0",
		"schedule=\"* * * * *\"",
		"id=cron-xyz",
		"- crew:alpha:1",
		"id=cron-old",
		"webhook: +1 -1 =0",
		"+ /hooks/a",
		"- /hooks/b",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestWriteReconcileResultHumanDryRunLabel(t *testing.T) {
	buf := &bytes.Buffer{}
	env := reconcileEnvelope{Agent: "alpha", DryRun: true}
	if err := writeReconcileResult(buf, env, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "Dry-run \"alpha\":") {
		t.Errorf("expected Dry-run header, got: %q", buf.String())
	}
}

func TestWithDefaultsPopulatesRunReconcile(t *testing.T) {
	d := runtimeDeps{}.withDefaults()
	if d.RunReconcile == nil {
		t.Errorf("RunReconcile default not set")
	}
}
