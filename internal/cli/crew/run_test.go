package crew

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResolveInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		inline    string
		path      string
		files     map[string][]byte
		wantErr   string
		wantBytes string
	}{
		{name: "default empty object", wantBytes: `{}`},
		{name: "inline valid", inline: `{"a":1}`, wantBytes: `{"a":1}`},
		{name: "inline invalid json", inline: "{not-json", wantErr: "not valid JSON"},
		{
			name:      "file valid",
			path:      "in.json",
			files:     map[string][]byte{"in.json": []byte(`{"k":"v"}`)},
			wantBytes: `{"k":"v"}`,
		},
		{
			name:    "file invalid json",
			path:    "bad.json",
			files:   map[string][]byte{"bad.json": []byte("not json")},
			wantErr: "not valid JSON",
		},
		{
			name:    "file missing",
			path:    "missing.json",
			wantErr: "read --input-file",
		},
		{
			name:    "both set",
			inline:  `{"a":1}`,
			path:    "in.json",
			wantErr: "mutually exclusive",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			readFile := func(p string) ([]byte, error) {
				if data, ok := tc.files[p]; ok {
					return data, nil
				}
				return nil, fs.ErrNotExist
			}
			got, err := resolveInput(tc.inline, tc.path, readFile)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got err=%v, want contains %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if string(got) != tc.wantBytes {
				t.Fatalf("got %q, want %q", got, tc.wantBytes)
			}
		})
	}
}

func TestNewRunCmdMutualExclusion(t *testing.T) {
	t.Parallel()

	deps := runDeps{
		Home:      t.TempDir(),
		Version:   "test",
		LoadAgent: func(dir string) (*AgentMeta, error) { return nil, fs.ErrNotExist },
	}
	cmd := newRunCmdWith(deps)
	cmd.SetArgs([]string{"demo", "--input", "{}", "--input-file", "x.json"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected mutual-exclusion error")
	}
}

func TestRunAgentNotFound(t *testing.T) {
	t.Parallel()

	deps := runDeps{
		Home:    t.TempDir(),
		Version: "test",
		LoadAgent: func(dir string) (*AgentMeta, error) {
			return nil, fmt.Errorf("load agent %s: read agent.yaml: %w", dir, fs.ErrNotExist)
		},
		Stdout: &bytes.Buffer{},
	}
	var stderr bytes.Buffer
	deps.Stderr = &stderr
	code := Run(context.Background(), deps, "ghost", runFlags{Timeout: time.Second})
	if code != ExitInvalidArgs {
		t.Fatalf("got exit %d, want %d", code, ExitInvalidArgs)
	}
	if !strings.Contains(stderr.String(), `crew member "ghost" not found`) {
		t.Fatalf("stderr missing not-found message: %s", stderr.String())
	}
}

func TestRunInvalidInput(t *testing.T) {
	t.Parallel()

	deps := runDeps{
		Home:    t.TempDir(),
		Version: "test",
	}
	var stderr bytes.Buffer
	deps.Stderr = &stderr
	code := Run(context.Background(), deps, "demo", runFlags{
		Input:   "{broken",
		Timeout: time.Second,
	})
	if code != ExitInvalidArgs {
		t.Fatalf("got exit %d, want %d", code, ExitInvalidArgs)
	}
}

func TestRunSocketSuccess(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := writeTestAgent(t, home, "demo", ExecutionModeService)
	_ = dir

	listener, sockPath := startUnixListener(t, home, "demo")
	defer listener.Close()

	go serveDaemon(t, listener, daemonScript{
		handshake: handshakeOK,
		runResult: mustJSON(t, runResult{
			Output:     runOutput{Text: "hello world"},
			TraceID:    "trace-abc",
			Status:     "ok",
			DurationMs: 42,
		}),
	})

	var stdout, stderr bytes.Buffer
	deps := runDeps{
		Home:       home,
		Version:    "v1.0.0",
		Stdout:     &stdout,
		Stderr:     &stderr,
		DialSocket: unixDialer(sockPath),
	}
	code := Run(context.Background(), deps, "demo", runFlags{Timeout: 2 * time.Second})
	if code != ExitOK {
		t.Fatalf("got exit %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello world") {
		t.Fatalf("stdout missing output: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "trace_id=trace-abc") {
		t.Fatalf("stderr missing trace_id: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "status=ok") {
		t.Fatalf("stderr missing status: %q", stderr.String())
	}
}

func TestRunSocketBusinessError(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeService)
	listener, sockPath := startUnixListener(t, home, "demo")
	defer listener.Close()

	go serveDaemon(t, listener, daemonScript{
		handshake: handshakeOK,
		runResult: mustJSON(t, runResult{
			Output:  runOutput{Text: "boom"},
			TraceID: "err-1",
			Status:  "err",
		}),
	})

	var stdout, stderr bytes.Buffer
	deps := runDeps{
		Home:       home,
		Version:    "v1.0.0",
		Stdout:     &stdout,
		Stderr:     &stderr,
		DialSocket: unixDialer(sockPath),
	}
	code := Run(context.Background(), deps, "demo", runFlags{Timeout: 2 * time.Second})
	if code != ExitBusinessErr {
		t.Fatalf("got exit %d, want %d; stderr=%s", code, ExitBusinessErr, stderr.String())
	}
}

func TestRunSocketVersionMismatch(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeService)
	listener, sockPath := startUnixListener(t, home, "demo")
	defer listener.Close()

	go serveDaemon(t, listener, daemonScript{
		handshakeErr: &rpcError{Code: errCodeVersionMismatch, Message: "mismatch"},
	})

	var stdout, stderr bytes.Buffer
	deps := runDeps{
		Home:       home,
		Version:    "v1.0.0",
		Stdout:     &stdout,
		Stderr:     &stderr,
		DialSocket: unixDialer(sockPath),
	}
	code := Run(context.Background(), deps, "demo", runFlags{Timeout: 2 * time.Second})
	if code != ExitVersionMismatch {
		t.Fatalf("got exit %d, want %d; stderr=%s", code, ExitVersionMismatch, stderr.String())
	}
	if !strings.Contains(stderr.String(), "version mismatch") {
		t.Fatalf("stderr missing version-mismatch message: %q", stderr.String())
	}
}

func TestRunSocketFallbackToSubprocess(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeService)

	// Intentionally do not start a listener, so the dial fails and we fall
	// back to subprocess.
	var commands [][]string
	deps := runDeps{
		Home:    home,
		Version: "v1.0.0",
		DialSocket: func(ctx context.Context, path string) (net.Conn, error) {
			return nil, errors.New("no listener")
		},
		LookPath:    func(s string) (string, error) { return "/usr/bin/stub-crew", nil },
		MakeCommand: fakeCommand(&commands, 0, `{"output":{"text":"from-sub"},"trace_id":"sub-1","status":"ok"}`, ""),
	}
	var stdout, stderr bytes.Buffer
	deps.Stdout = &stdout
	deps.Stderr = &stderr
	code := Run(context.Background(), deps, "demo", runFlags{Timeout: 2 * time.Second})
	if code != ExitOK {
		t.Fatalf("got exit %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "daemon not responding") {
		t.Fatalf("stderr missing fallback warning: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "from-sub") {
		t.Fatalf("stdout missing subprocess output: %q", stdout.String())
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 subprocess invocation, got %d", len(commands))
	}
	if commands[0][1] != "--agent" || commands[0][2] != "demo" {
		t.Fatalf("unexpected subprocess args: %v", commands[0])
	}
}

func TestRunOnDemandNeverDials(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeOnDemand)

	dialCalled := false
	var commands [][]string
	deps := runDeps{
		Home:    home,
		Version: "v1.0.0",
		DialSocket: func(ctx context.Context, path string) (net.Conn, error) {
			dialCalled = true
			return nil, errors.New("should not be called")
		},
		LookPath:    func(s string) (string, error) { return "/usr/bin/stub-crew", nil },
		MakeCommand: fakeCommand(&commands, 0, `{"output":{"text":"ok"},"trace_id":"t1","status":"ok"}`, ""),
	}
	var stdout, stderr bytes.Buffer
	deps.Stdout = &stdout
	deps.Stderr = &stderr
	code := Run(context.Background(), deps, "demo", runFlags{Timeout: 2 * time.Second})
	if code != ExitOK {
		t.Fatalf("got exit %d, want 0; stderr=%s", code, stderr.String())
	}
	if dialCalled {
		t.Fatal("dial must not be attempted in on-demand mode")
	}
}

func TestRunSubprocessBusinessError(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeOnDemand)

	var commands [][]string
	deps := runDeps{
		Home:    home,
		Version: "v1.0.0",
		DialSocket: func(ctx context.Context, path string) (net.Conn, error) {
			return nil, errors.New("never")
		},
		LookPath:    func(s string) (string, error) { return "/usr/bin/stub-crew", nil },
		MakeCommand: fakeCommand(&commands, 1, `{"output":{"text":"nope"},"trace_id":"fail","status":"err"}`, ""),
	}
	var stdout, stderr bytes.Buffer
	deps.Stdout = &stdout
	deps.Stderr = &stderr
	code := Run(context.Background(), deps, "demo", runFlags{Timeout: 2 * time.Second})
	if code != ExitBusinessErr {
		t.Fatalf("got exit %d, want %d", code, ExitBusinessErr)
	}
}

func TestRunSubprocessInternalError(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeOnDemand)

	var commands [][]string
	deps := runDeps{
		Home:        home,
		Version:     "v1.0.0",
		DialSocket:  func(ctx context.Context, path string) (net.Conn, error) { return nil, errors.New("never") },
		LookPath:    func(s string) (string, error) { return "/usr/bin/stub-crew", nil },
		MakeCommand: fakeCommand(&commands, 42, "", "boom"),
	}
	var stdout, stderr bytes.Buffer
	deps.Stdout = &stdout
	deps.Stderr = &stderr
	code := Run(context.Background(), deps, "demo", runFlags{Timeout: 2 * time.Second})
	if code != ExitInternal {
		t.Fatalf("got exit %d, want %d", code, ExitInternal)
	}
}

func TestRunSubprocessBinaryNotFound(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeOnDemand)

	deps := runDeps{
		Home:       home,
		Version:    "v1.0.0",
		DialSocket: func(ctx context.Context, path string) (net.Conn, error) { return nil, errors.New("never") },
		LookPath:   func(s string) (string, error) { return "", exec.ErrNotFound },
	}
	var stderr bytes.Buffer
	deps.Stderr = &stderr
	code := Run(context.Background(), deps, "demo", runFlags{Timeout: time.Second})
	if code != ExitInternal {
		t.Fatalf("got exit %d, want %d", code, ExitInternal)
	}
	if !strings.Contains(stderr.String(), "shipyard crew install") {
		t.Fatalf("stderr missing install hint: %q", stderr.String())
	}
}

func TestRunJSONOutput(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeOnDemand)

	var commands [][]string
	deps := runDeps{
		Home:        home,
		Version:     "v1.0.0",
		DialSocket:  func(ctx context.Context, path string) (net.Conn, error) { return nil, errors.New("never") },
		LookPath:    func(s string) (string, error) { return "/usr/bin/stub", nil },
		MakeCommand: fakeCommand(&commands, 0, `{"output":{"text":"hi"},"trace_id":"t","status":"ok","duration_ms":5}`, ""),
	}
	var stdout, stderr bytes.Buffer
	deps.Stdout = &stdout
	deps.Stderr = &stderr
	code := Run(context.Background(), deps, "demo", runFlags{Timeout: time.Second, JSON: true})
	if code != ExitOK {
		t.Fatalf("got exit %d, stderr=%s", code, stderr.String())
	}
	var got runResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil {
		t.Fatalf("stdout is not JSON: %s", stdout.String())
	}
	if got.Output.Text != "hi" {
		t.Fatalf("unexpected output: %+v", got)
	}
}

func TestExitError(t *testing.T) {
	t.Parallel()

	err := &ExitError{Code: 60}
	if err.ExitCode() != 60 {
		t.Fatalf("got %d, want 60", err.ExitCode())
	}
	if err.Error() != "exit code 60" {
		t.Fatalf("unexpected error string: %q", err.Error())
	}
	err2 := &ExitError{Code: 2, Message: "bad"}
	if err2.Error() != "bad" {
		t.Fatalf("got %q, want %q", err2.Error(), "bad")
	}
}

func TestNewRunCmdDefaults(t *testing.T) {
	t.Parallel()

	cmd := NewRunCmd()
	if cmd.Use != "run <name>" {
		t.Fatalf("unexpected Use: %q", cmd.Use)
	}
	if f := cmd.Flag("timeout"); f == nil || f.DefValue == "" {
		t.Fatalf("--timeout flag not registered")
	}
	for _, name := range []string{"input", "input-file", "json", "timeout"} {
		if cmd.Flag(name) == nil {
			t.Fatalf("flag %q missing", name)
		}
	}
}

func TestRunCobraReturnsExitError(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeOnDemand)
	deps := runDeps{
		Home:       home,
		Version:    "v1",
		DialSocket: func(ctx context.Context, path string) (net.Conn, error) { return nil, errors.New("n") },
		LookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
	}
	cmd := newRunCmdWith(deps)
	cmd.SetArgs([]string{"demo"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if ee.Code != ExitInternal {
		t.Fatalf("got code %d, want %d", ee.Code, ExitInternal)
	}
}

func TestLoadAgentMeta(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeService)

	meta, err := loadAgentMeta(filepath.Join(home, "crew", "demo"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if meta.Name != "demo" || meta.ExecutionMode != ExecutionModeService {
		t.Fatalf("unexpected meta: %+v", meta)
	}

	_, err = loadAgentMeta(filepath.Join(home, "crew", "missing"))
	if err == nil {
		t.Fatal("expected error for missing dir")
	}

	// Invalid execution mode.
	badDir := filepath.Join(home, "crew", "bad")
	_ = os.MkdirAll(badDir, 0o755)
	_ = os.WriteFile(filepath.Join(badDir, "agent.yaml"), []byte(`name: bad
execution:
  mode: what
`), 0o644)
	if _, err := loadAgentMeta(badDir); err == nil {
		t.Fatal("expected error for invalid mode")
	}

	// Missing name.
	nmDir := filepath.Join(home, "crew", "noname")
	_ = os.MkdirAll(nmDir, 0o755)
	_ = os.WriteFile(filepath.Join(nmDir, "agent.yaml"), []byte(`execution:
  mode: on-demand
`), 0o644)
	if _, err := loadAgentMeta(nmDir); err == nil {
		t.Fatal("expected error for missing name")
	}

	// Malformed YAML.
	myDir := filepath.Join(home, "crew", "malformed")
	_ = os.MkdirAll(myDir, 0o755)
	_ = os.WriteFile(filepath.Join(myDir, "agent.yaml"), []byte("::not yaml::"), 0o644)
	if _, err := loadAgentMeta(myDir); err == nil {
		t.Fatal("expected error for malformed yaml")
	}
}

func TestRunSocketHandshakeNonVersionError(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTestAgent(t, home, "demo", ExecutionModeService)
	listener, sockPath := startUnixListener(t, home, "demo")
	defer listener.Close()

	go serveDaemon(t, listener, daemonScript{
		handshakeErr: &rpcError{Code: -32000, Message: "boom"},
	})

	var stderr bytes.Buffer
	deps := runDeps{
		Home:       home,
		Version:    "v1",
		Stderr:     &stderr,
		DialSocket: unixDialer(sockPath),
	}
	code := Run(context.Background(), deps, "demo", runFlags{Timeout: 2 * time.Second})
	if code != ExitInternal {
		t.Fatalf("got exit %d, want %d", code, ExitInternal)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func writeTestAgent(t *testing.T, home, name string, mode string) string {
	t.Helper()
	dir := filepath.Join(home, "crew", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Intentionally minimal YAML matching the crew parser contract.
	yaml := fmt.Sprintf(`schema_version: "1"
name: %s
description: test
backend:
  type: cli
  command: ["echo"]
execution:
  mode: %s
  pool: default
conversation:
  mode: stateless
triggers: []
tools: []
`, name, mode)
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("# prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func startUnixListener(t *testing.T, home, name string) (net.Listener, string) {
	t.Helper()
	runDir := filepath.Join(home, "run", "crew")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sockPath := filepath.Join(runDir, name+".sock")
	// macOS has a ~104-byte unix socket path limit; t.TempDir() paths can
	// exceed that. Use a short path under os.TempDir when necessary.
	if runtime.GOOS == "darwin" && len(sockPath) > 100 {
		alt, err := os.MkdirTemp("", "crew-sock-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(alt) })
		sockPath = filepath.Join(alt, name+".sock")
	}
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close(); os.Remove(sockPath) })
	return listener, sockPath
}

func unixDialer(path string) func(ctx context.Context, _ string) (net.Conn, error) {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}
}

type daemonScript struct {
	handshake    bool
	handshakeErr *rpcError
	runResult    json.RawMessage
	runErr       *rpcError
}

var handshakeOK = true

func serveDaemon(t *testing.T, listener net.Listener, script daemonScript) {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 1<<20), 1<<20)
	writer := bufio.NewWriter(conn)

	// handshake
	if !reader.Scan() {
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(reader.Bytes(), &req)
	if script.handshakeErr != nil {
		writeRPCResponse(writer, req.ID, nil, script.handshakeErr)
		return
	}
	writeRPCResponse(writer, req.ID, json.RawMessage(`{"version":"ok"}`), nil)

	// run
	if !reader.Scan() {
		return
	}
	_ = json.Unmarshal(reader.Bytes(), &req)
	if script.runErr != nil {
		writeRPCResponse(writer, req.ID, nil, script.runErr)
		return
	}
	writeRPCResponse(writer, req.ID, script.runResult, nil)
}

func writeRPCResponse(w *bufio.Writer, id json.RawMessage, result json.RawMessage, rerr *rpcError) {
	payload := map[string]any{
		"jsonrpc": jsonrpcVersion,
		"id":      id,
	}
	if rerr != nil {
		payload["error"] = rerr
	} else {
		payload["result"] = result
	}
	data, _ := json.Marshal(payload)
	w.Write(append(data, '\n'))
	w.Flush()
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fakeCommand returns a MakeCommand that shells into `env` + the current test
// binary to simulate a child process. Because we want hermetic tests we can't
// rely on a real binary, so we use `sh -c` to emit stdout/stderr and exit
// with the desired code. The first captured args[0] is "sh".
func fakeCommand(commands *[][]string, exitCode int, stdout, stderr string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		recorded := append([]string{name}, args...)
		*commands = append(*commands, recorded)
		script := fmt.Sprintf(`printf %q; >&2 printf %q; exit %d`, stdout, stderr, exitCode)
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		return cmd
	}
}
