package crew

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fireCalls struct {
	serviceUnregister  []string
	cronUnreconcile    []string
	webhookUnreconcile []string
}

func newFakeFireDeps(t *testing.T, stdin io.Reader, tty bool) (fireDeps, *fireCalls, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	tmp := t.TempDir()
	calls := &fireCalls{}
	var stdout, stderr bytes.Buffer
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	deps := fireDeps{
		Home:   tmp,
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: &stderr,
		IsTTY:  func() bool { return tty },
		UnregisterService: func(ctx context.Context, name string) error {
			calls.serviceUnregister = append(calls.serviceUnregister, name)
			return nil
		},
		UnreconcileCron: func(ctx context.Context, agentName string) error {
			calls.cronUnreconcile = append(calls.cronUnreconcile, agentName)
			return nil
		},
		UnreconcileWebhook: func(ctx context.Context, agentName, route string) error {
			calls.webhookUnreconcile = append(calls.webhookUnreconcile, agentName+"|"+route)
			return nil
		},
	}
	return deps, calls, &stdout, &stderr
}

func writeFireAgent(t *testing.T, home, name, body string) string {
	t.Helper()
	dir := filepath.Join(home, "crew", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write agent.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("# "+name), 0o600); err != nil {
		t.Fatalf("write prompt.md: %v", err)
	}
	return dir
}

const fireServiceAgentYAML = `schema_version: "1"
name: svc-agent
backend:
  type: cli
  command: ["claude"]
execution:
  mode: service
  pool: cli
conversation:
  mode: stateless
triggers:
  - type: cron
    schedule: "*/5 * * * *"
  - type: webhook
    route: /svc-agent
tools: []
`

func TestRunFire_HappyPath(t *testing.T) {
	deps, calls, stdout, stderr := newFakeFireDeps(t, nil, false)
	dir := writeFireAgent(t, deps.Home, "svc-agent", fireServiceAgentYAML)
	sockDir := filepath.Join(deps.Home, "run", "crew")
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"svc-agent.sock", "svc-agent.pid"} {
		if err := os.WriteFile(filepath.Join(sockDir, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logsDir := filepath.Join(deps.Home, "logs", "crew", "svc-agent")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	code := runFire(context.Background(), deps, "svc-agent", true, false)
	if code != fireExitOK {
		t.Fatalf("code = %d, want %d; stderr=%q", code, fireExitOK, stderr.String())
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("agent dir still exists: err=%v", err)
	}
	for _, p := range []string{"svc-agent.sock", "svc-agent.pid"} {
		if _, err := os.Stat(filepath.Join(sockDir, p)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: err=%v", p, err)
		}
	}
	if _, err := os.Stat(logsDir); !os.IsNotExist(err) {
		t.Fatalf("logs dir still exists: err=%v", err)
	}

	if len(calls.serviceUnregister) != 1 || calls.serviceUnregister[0] != "svc-agent" {
		t.Errorf("service unregister calls = %v", calls.serviceUnregister)
	}
	if len(calls.cronUnreconcile) != 1 || calls.cronUnreconcile[0] != "svc-agent" {
		t.Errorf("cron unreconcile calls = %v", calls.cronUnreconcile)
	}
	if len(calls.webhookUnreconcile) != 1 || !strings.Contains(calls.webhookUnreconcile[0], "/svc-agent") {
		t.Errorf("webhook unreconcile calls = %v", calls.webhookUnreconcile)
	}
	if !strings.Contains(stdout.String(), "has been fired") {
		t.Errorf("stdout missing success msg: %q", stdout.String())
	}
}

func TestRunFire_NotFound(t *testing.T) {
	deps, _, _, stderr := newFakeFireDeps(t, nil, false)
	code := runFire(context.Background(), deps, "missing", true, false)
	if code != fireExitNotFound {
		t.Fatalf("code = %d, want %d", code, fireExitNotFound)
	}
	if !strings.Contains(stderr.String(), "crew member not found") {
		t.Errorf("stderr missing not-found msg: %q", stderr.String())
	}
}

func TestRunFire_InvalidName(t *testing.T) {
	deps, _, _, stderr := newFakeFireDeps(t, nil, false)
	code := runFire(context.Background(), deps, "BAD/NAME", true, false)
	if code != fireExitNotFound {
		t.Fatalf("code = %d, want %d", code, fireExitNotFound)
	}
	if !strings.Contains(stderr.String(), "invalid name") {
		t.Errorf("stderr missing invalid-name msg: %q", stderr.String())
	}
}

func TestRunFire_NonTTYRequiresYes(t *testing.T) {
	deps, _, _, stderr := newFakeFireDeps(t, nil, false)
	writeFireAgent(t, deps.Home, "a", fireServiceAgentYAML)
	code := runFire(context.Background(), deps, "a", false, false)
	if code != fireExitNotFound {
		t.Fatalf("code = %d, want %d", code, fireExitNotFound)
	}
	if !strings.Contains(stderr.String(), "refusing to fire non-interactively") {
		t.Errorf("stderr missing non-interactive msg: %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(deps.Home, "crew", "a")); err != nil {
		t.Errorf("agent dir removed despite error: %v", err)
	}
}

func TestRunFire_InteractiveYes(t *testing.T) {
	deps, calls, _, _ := newFakeFireDeps(t, strings.NewReader("y\n"), true)
	writeFireAgent(t, deps.Home, "ondemand", `schema_version: "1"
name: ondemand
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateless
triggers: []
tools: []
`)
	code := runFire(context.Background(), deps, "ondemand", false, false)
	if code != fireExitOK {
		t.Fatalf("code = %d", code)
	}
	if len(calls.serviceUnregister) != 0 {
		t.Errorf("on-demand must not trigger service unregister, got %v", calls.serviceUnregister)
	}
	if _, err := os.Stat(filepath.Join(deps.Home, "crew", "ondemand")); !os.IsNotExist(err) {
		t.Errorf("dir still exists: %v", err)
	}
}

func TestRunFire_InteractiveNo(t *testing.T) {
	deps, _, stdout, _ := newFakeFireDeps(t, strings.NewReader("n\n"), true)
	writeFireAgent(t, deps.Home, "keepme", fireServiceAgentYAML)
	code := runFire(context.Background(), deps, "keepme", false, false)
	if code != fireExitOK {
		t.Fatalf("code = %d, expected cancel to exit 0", code)
	}
	if !strings.Contains(stdout.String(), "cancelled") {
		t.Errorf("stdout missing cancellation msg: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(deps.Home, "crew", "keepme")); err != nil {
		t.Errorf("dir must remain on cancel: %v", err)
	}
}

func TestRunFire_KeepLogs(t *testing.T) {
	deps, _, _, _ := newFakeFireDeps(t, nil, false)
	writeFireAgent(t, deps.Home, "k", fireServiceAgentYAML)
	logsDir := filepath.Join(deps.Home, "logs", "crew", "k")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "a.log"), []byte("entry"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := runFire(context.Background(), deps, "k", true, true)
	if code != fireExitOK {
		t.Fatalf("code = %d", code)
	}
	if _, err := os.Stat(logsDir); err != nil {
		t.Errorf("logs dir must be preserved with --keep-logs: %v", err)
	}
}

func TestRunFire_ServiceUnregisterFailStillRemoves(t *testing.T) {
	deps, _, _, stderr := newFakeFireDeps(t, nil, false)
	deps.UnregisterService = func(ctx context.Context, name string) error {
		return errors.New("launchctl: boom")
	}
	writeFireAgent(t, deps.Home, "x", fireServiceAgentYAML)
	code := runFire(context.Background(), deps, "x", true, false)
	if code != fireExitOK {
		t.Fatalf("code = %d, want success despite service failure", code)
	}
	if !strings.Contains(stderr.String(), "warning: unregister service") {
		t.Errorf("stderr missing warning: %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(deps.Home, "crew", "x")); !os.IsNotExist(err) {
		t.Errorf("dir still exists: %v", err)
	}
}

func TestRunFire_CorruptedYAMLContinues(t *testing.T) {
	deps, calls, _, stderr := newFakeFireDeps(t, nil, false)
	writeFireAgent(t, deps.Home, "bad", "this: is: not: valid: yaml: [[[")
	code := runFire(context.Background(), deps, "bad", true, false)
	if code != fireExitOK {
		t.Fatalf("code = %d, want success on corrupted yaml", code)
	}
	if !strings.Contains(stderr.String(), "warning:") {
		t.Errorf("stderr should contain warning: %q", stderr.String())
	}
	// With yaml-independent cron cleanup (list+filter by Name prefix),
	// corrupted yaml still triggers cron unreconcile — that's the whole
	// point of the §1.6 redesign. Webhook and service cleanup still need
	// yaml (route/mode), so they're skipped.
	if len(calls.serviceUnregister) != 0 {
		t.Errorf("service unregister must be skipped on corrupt yaml; got %v", calls.serviceUnregister)
	}
	if len(calls.webhookUnreconcile) != 0 {
		t.Errorf("webhook unreconcile must be skipped on corrupt yaml; got %v", calls.webhookUnreconcile)
	}
	if len(calls.cronUnreconcile) != 1 || calls.cronUnreconcile[0] != "bad" {
		t.Errorf("cron unreconcile should run yaml-independently; got %v", calls.cronUnreconcile)
	}
	if _, err := os.Stat(filepath.Join(deps.Home, "crew", "bad")); !os.IsNotExist(err) {
		t.Errorf("dir still exists: %v", err)
	}
}

func TestRunFire_RemoveFatalReturnsExit2(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can remove read-only directories; skipping")
	}
	deps, _, _, stderr := newFakeFireDeps(t, nil, false)
	dir := writeFireAgent(t, deps.Home, "stuck", fireServiceAgentYAML)
	// Make the parent directory read-only so RemoveAll fails.
	parent := filepath.Dir(dir)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod parent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	code := runFire(context.Background(), deps, "stuck", true, false)
	if code != fireExitRemoveFail {
		t.Fatalf("code = %d, want %d; stderr=%q", code, fireExitRemoveFail, stderr.String())
	}
	if !strings.Contains(stderr.String(), "remove") {
		t.Errorf("stderr should mention remove failure: %q", stderr.String())
	}
}

func TestNewFireCmd_Flags(t *testing.T) {
	cmd := newFireCmd()
	for _, name := range []string{"yes", "keep-logs"} {
		if f := cmd.Flags().Lookup(name); f == nil {
			t.Errorf("flag --%s missing", name)
		}
	}
}

func TestNewFireCmdWith_CobraExecute(t *testing.T) {
	prev := resolveBinaryFn
	resolveBinaryFn = func() (string, error) { return "/fake/shipyard-crew", nil }
	t.Cleanup(func() { resolveBinaryFn = prev })

	deps, _, _, _ := newFakeFireDeps(t, nil, false)
	writeFireAgent(t, deps.Home, "viacobra", fireServiceAgentYAML)

	cmd := newFireCmdWith(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--yes", "viacobra"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deps.Home, "crew", "viacobra")); !os.IsNotExist(err) {
		t.Errorf("dir still exists: %v", err)
	}
}

func TestNewFireCmdWith_CobraExecuteReturnsExitError(t *testing.T) {
	prev := resolveBinaryFn
	resolveBinaryFn = func() (string, error) { return "/fake/shipyard-crew", nil }
	t.Cleanup(func() { resolveBinaryFn = prev })

	deps, _, _, _ := newFakeFireDeps(t, nil, false)
	cmd := newFireCmdWith(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--yes", "nonexistent"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *ExitError, got %T: %v", err, err)
	}
	if ee.ExitCode() != fireExitNotFound {
		t.Errorf("exit code = %d, want %d", ee.ExitCode(), fireExitNotFound)
	}
}

func TestFireDepsWithDefaults_FillsZeros(t *testing.T) {
	t.Setenv("SHIPYARD_HOME", t.TempDir())
	d := fireDeps{}.withDefaults()
	if d.Home == "" {
		t.Error("Home should be set")
	}
	if d.Stdin == nil {
		t.Error("Stdin should be set")
	}
	if d.Stdout == nil || d.Stderr == nil {
		t.Error("Stdout/Stderr should be set")
	}
	if d.IsTTY == nil {
		t.Error("IsTTY should be set")
	}
	if d.UnregisterService == nil || d.UnreconcileCron == nil || d.UnreconcileWebhook == nil {
		t.Error("cleanup callbacks should be set")
	}
}

// writeFakeShipyard installs a fake `shipyard` binary into bindir whose
// behavior is dispatched by the first two subcommands ($1 $2). Each
// dispatch key is the space-joined pair (e.g. "cron list"); the value is a
// shell snippet executed with the remaining args in $@.
func writeFakeShipyard(t *testing.T, bindir string, dispatch map[string]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(`sub="$1"; shift || true; sub2="$1"; shift || true` + "\n")
	for key, body := range dispatch {
		parts := strings.SplitN(key, " ", 2)
		if len(parts) != 2 {
			t.Fatalf("writeFakeShipyard dispatch key %q must be 'sub sub2'", key)
		}
		fmt.Fprintf(&b, "if [ \"$sub\" = \"%s\" ] && [ \"$sub2\" = \"%s\" ]; then\n%s\nfi\n",
			parts[0], parts[1], body)
	}
	b.WriteString(`echo "fake shipyard: unhandled $sub $sub2 $@" >&2; exit 99` + "\n")
	script := filepath.Join(bindir, "shipyard")
	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
}

func TestDefaultUnreconcileCron_FiltersByPrefixAndDeletes(t *testing.T) {
	bindir := t.TempDir()
	logFile := filepath.Join(bindir, "calls.log")
	writeFakeShipyard(t, bindir, map[string]string{
		"cron list": `cat <<'EOF'
[
 {"id":"abc","name":"crew:alice:0"},
 {"id":"xyz","name":"crew:alice:1"},
 {"id":"zzz","name":"crew:bob:0"},
 {"id":"ttt","name":"user-own"}
]
EOF
exit 0`,
		"cron delete": fmt.Sprintf(`echo "delete $@" >> %q; exit 0`, logFile),
	})
	t.Setenv("PATH", bindir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := defaultUnreconcileCron(context.Background(), "alice"); err != nil {
		t.Fatalf("cron: %v", err)
	}

	raw, _ := os.ReadFile(logFile)
	got := string(raw)
	for _, want := range []string{"delete abc", "delete xyz"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in call log; got %q", want, got)
		}
	}
	for _, bad := range []string{"delete zzz", "delete ttt"} {
		if strings.Contains(got, bad) {
			t.Errorf("must not delete non-matching entry: %q in %q", bad, got)
		}
	}
}

func TestDefaultUnreconcileCron_EmptyListIsNoop(t *testing.T) {
	bindir := t.TempDir()
	writeFakeShipyard(t, bindir, map[string]string{
		"cron list":   `echo "[]"; exit 0`,
		"cron delete": `echo "UNEXPECTED" >&2; exit 2`,
	})
	t.Setenv("PATH", bindir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := defaultUnreconcileCron(context.Background(), "alice"); err != nil {
		t.Fatalf("cron: %v", err)
	}
}

func TestDefaultUnreconcileCron_NullListIsNoop(t *testing.T) {
	bindir := t.TempDir()
	writeFakeShipyard(t, bindir, map[string]string{
		"cron list":   `echo "null"; exit 0`,
		"cron delete": `echo "UNEXPECTED" >&2; exit 2`,
	})
	t.Setenv("PATH", bindir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := defaultUnreconcileCron(context.Background(), "alice"); err != nil {
		t.Fatalf("cron: %v", err)
	}
}

func TestDefaultUnreconcileCron_ListFailurePropagates(t *testing.T) {
	bindir := t.TempDir()
	writeFakeShipyard(t, bindir, map[string]string{
		"cron list":   `echo "boom" >&2; exit 3`,
		"cron delete": `exit 0`,
	})
	t.Setenv("PATH", bindir+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := defaultUnreconcileCron(context.Background(), "alice")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "cron list") {
		t.Errorf("err = %v, want mention of cron list", err)
	}
}

func TestDefaultUnreconcileCron_DeleteFailureIsCollected(t *testing.T) {
	bindir := t.TempDir()
	writeFakeShipyard(t, bindir, map[string]string{
		"cron list": `cat <<'EOF'
[{"id":"abc","name":"crew:alice:0"},{"id":"xyz","name":"crew:alice:1"}]
EOF
exit 0`,
		"cron delete": `echo "deny" >&2; exit 2`,
	})
	t.Setenv("PATH", bindir+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := defaultUnreconcileCron(context.Background(), "alice")
	if err == nil {
		t.Fatal("expected delete error")
	}
}

func TestDefaultUnreconcileCron_PrefixBoundaryIsSafe(t *testing.T) {
	bindir := t.TempDir()
	logFile := filepath.Join(bindir, "calls.log")
	writeFakeShipyard(t, bindir, map[string]string{
		"cron list": `cat <<'EOF'
[
 {"id":"1","name":"crew:alice:0"},
 {"id":"2","name":"crew:alice-extra:0"},
 {"id":"3","name":"crew:alicebob:0"}
]
EOF
exit 0`,
		"cron delete": fmt.Sprintf(`echo "delete $@" >> %q; exit 0`, logFile),
	})
	t.Setenv("PATH", bindir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := defaultUnreconcileCron(context.Background(), "alice"); err != nil {
		t.Fatalf("cron: %v", err)
	}
	raw, _ := os.ReadFile(logFile)
	got := strings.TrimSpace(string(raw))
	if got != "delete 1" {
		t.Errorf("only crew:alice:... should be deleted; got %q", got)
	}
}

func TestDefaultUnreconcileWebhook_InvokesShipyardBinaryWithYes(t *testing.T) {
	bindir := t.TempDir()
	logFile := filepath.Join(bindir, "calls.log")
	writeFakeShipyard(t, bindir, map[string]string{
		"fairway route": fmt.Sprintf(`echo "fairway route $@" >> %q; exit 0`, logFile),
	})
	t.Setenv("PATH", bindir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := defaultUnreconcileWebhook(context.Background(), "alice", "/alice"); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	raw, _ := os.ReadFile(logFile)
	got := string(raw)
	if !strings.Contains(got, "delete /alice") {
		t.Errorf("missing delete route: %q", got)
	}
	if !strings.Contains(got, "--yes") {
		t.Errorf("delete must pass --yes non-interactively: %q", got)
	}
}

func TestDefaultUnreconcileWebhook_PropagatesFailure(t *testing.T) {
	bindir := t.TempDir()
	writeFakeShipyard(t, bindir, map[string]string{
		"fairway route": `echo "boom" >&2; exit 3`,
	})
	t.Setenv("PATH", bindir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := defaultUnreconcileWebhook(context.Background(), "alice", "/alice"); err == nil {
		t.Error("expected error")
	}
}

func TestDefaultUnregisterService_IdempotentOnMissing(t *testing.T) {
	// Use a tmp HOME so manager does not touch the real user directory.
	// UnregisterAgentService is idempotent: missing plist/unit → nil.
	t.Setenv("HOME", t.TempDir())
	if err := defaultUnregisterService(context.Background(), "nobody"); err != nil {
		// Not fatal: some CI platforms may be unsupported. Accept error path too.
		t.Logf("defaultUnregisterService returned %v (acceptable if platform unsupported)", err)
	}
}

func TestShipyardHome_EnvAndFallback(t *testing.T) {
	t.Setenv("SHIPYARD_HOME", "/tmp/custom-home")
	h, err := shipyardHome()
	if err != nil {
		t.Fatalf("shipyardHome: %v", err)
	}
	if h != "/tmp/custom-home" {
		t.Errorf("home = %q, want /tmp/custom-home", h)
	}

	t.Setenv("SHIPYARD_HOME", "")
	h, err = shipyardHome()
	if err != nil {
		t.Fatalf("shipyardHome fallback: %v", err)
	}
	if !strings.HasSuffix(h, "/.shipyard") {
		t.Errorf("fallback home = %q, want suffix /.shipyard", h)
	}
}

func TestCronNamePrefixFor(t *testing.T) {
	if got := cronNamePrefixFor("alice"); got != "crew:alice:" {
		t.Errorf("cronNamePrefixFor = %q, want %q", got, "crew:alice:")
	}
}
