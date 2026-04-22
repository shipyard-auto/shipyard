package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validAgentYAML = `schema_version: "1"
name: %s
description: test agent
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

func writeAgent(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, "crew", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(fmt.Sprintf(validAgentYAML, name)), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("system prompt"), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	return dir
}

// shortTempDir returns a short /tmp path to stay under macOS's 104-byte
// Unix-socket limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "crewd")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func baseOpts(t *testing.T, name string) Options {
	t.Helper()
	root := shortTempDir(t)
	agentDir := writeAgent(t, root, name)
	return Options{
		AgentName: name,
		AgentDir:  agentDir,
		RunDir:    filepath.Join(root, "run"),
		Version:   "1.0.0",
	}
}

func dialSocket(t *testing.T, path string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var (
		c   net.Conn
		err error
	)
	for time.Now().Before(deadline) {
		c, err = net.Dial("unix", path)
		if err == nil {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("dial %s: %v", path, err)
	return nil
}

func sendJSON(t *testing.T, c net.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	b = append(b, '\n')
	if _, err := c.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readJSON(t *testing.T, sc *bufio.Scanner) map[string]any {
	t.Helper()
	if !sc.Scan() {
		t.Fatalf("scan failed: %v", sc.Err())
	}
	var m map[string]any
	if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v; line=%s", err, sc.Text())
	}
	return m
}

func TestRunValidatesOptions(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"no agent name", Options{AgentDir: "x", RunDir: "y"}},
		{"no agent dir", Options{AgentName: "a", RunDir: "y"}},
		{"no run dir", Options{AgentName: "a", AgentDir: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, err := Run(context.Background(), tc.opts)
			if code != ExitBuildRuntime {
				t.Errorf("code = %d, want %d", code, ExitBuildRuntime)
			}
			if err == nil {
				t.Errorf("expected error")
			}
		})
	}
}

func TestRunMissingAgentDir(t *testing.T) {
	opts := baseOpts(t, "ghost")
	opts.AgentDir = filepath.Join(shortTempDir(t), "nope")
	code, err := Run(context.Background(), opts)
	if code != ExitInvalidConfig {
		t.Errorf("code = %d, want %d", code, ExitInvalidConfig)
	}
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestRunAgentNameMismatch(t *testing.T) {
	opts := baseOpts(t, "alpha")
	opts.AgentName = "beta"
	code, err := Run(context.Background(), opts)
	if code != ExitInvalidConfig {
		t.Errorf("code = %d, want %d", code, ExitInvalidConfig)
	}
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunPIDConflict(t *testing.T) {
	opts := baseOpts(t, "promo")
	// Pre-create PID file with the current pid (always alive).
	if err := os.MkdirAll(opts.RunDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pidPath := filepath.Join(opts.RunDir, opts.AgentName+".pid")
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatalf("seed pid: %v", err)
	}
	code, err := Run(context.Background(), opts)
	if code != ExitAlreadyRunning {
		t.Errorf("code = %d, want %d", code, ExitAlreadyRunning)
	}
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestRunLifecycleHandshakeStatusShutdown(t *testing.T) {
	opts := baseOpts(t, "alpha")
	opts.Version = "1.0.0"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, err := Run(ctx, opts)
		done <- struct {
			code int
			err  error
		}{code, err}
	}()

	sockPath := filepath.Join(opts.RunDir, opts.AgentName+".sock")
	c := dialSocket(t, sockPath)
	defer c.Close()
	sc := bufio.NewScanner(c)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, 1<<20)

	sendJSON(t, c, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "handshake", "params": map[string]any{"version": "1.0.0"}})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp := readJSON(t, sc)
	if resp["error"] != nil {
		t.Fatalf("handshake err: %+v", resp["error"])
	}

	sendJSON(t, c, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "status"})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp = readJSON(t, sc)
	if resp["error"] != nil {
		t.Fatalf("status err: %+v", resp["error"])
	}
	result := resp["result"].(map[string]any)
	if result["agent"] != "alpha" {
		t.Errorf("agent = %v", result["agent"])
	}

	// Trigger shutdown via signal (cancel ctx).
	cancel()

	select {
	case r := <-done:
		if r.code != ExitOK {
			t.Errorf("exit = %d, err=%v, want %d", r.code, r.err, ExitOK)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon did not exit")
	}

	// PID file and socket should be cleaned up.
	pidPath := filepath.Join(opts.RunDir, opts.AgentName+".pid")
	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pid file not removed: %v", err)
	}
	if _, err := os.Stat(sockPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket file not removed: %v", err)
	}
}

func TestRunShutdownViaRPC(t *testing.T) {
	opts := baseOpts(t, "beta")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		code, _ := Run(ctx, opts)
		done <- code
	}()

	sockPath := filepath.Join(opts.RunDir, opts.AgentName+".sock")
	c := dialSocket(t, sockPath)
	defer c.Close()
	sc := bufio.NewScanner(c)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, 1<<20)

	sendJSON(t, c, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "handshake", "params": map[string]any{"version": "1.0.0"}})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_ = readJSON(t, sc)
	sendJSON(t, c, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp := readJSON(t, sc)
	if resp["error"] != nil {
		t.Fatalf("shutdown rpc err: %+v", resp["error"])
	}

	select {
	case code := <-done:
		if code != ExitOK {
			t.Errorf("exit = %d, want %d", code, ExitOK)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon did not exit after RPC shutdown")
	}
}

func TestRunExecutesRunRPC(t *testing.T) {
	opts := baseOpts(t, "delta")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		code, _ := Run(ctx, opts)
		done <- code
	}()

	sockPath := filepath.Join(opts.RunDir, opts.AgentName+".sock")
	c := dialSocket(t, sockPath)
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), 1<<20)

	sendJSON(t, c, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "handshake", "params": map[string]any{"version": "1.0.0"}})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_ = readJSON(t, sc)

	sendJSON(t, c, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "run",
		"params":  map[string]any{"input": map[string]any{"user": "hello"}},
	})
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp := readJSON(t, sc)
	if resp["error"] != nil {
		t.Fatalf("run err: %+v", resp["error"])
	}
	result := resp["result"].(map[string]any)
	if result["trace_id"] == "" {
		t.Errorf("trace_id is empty")
	}

	cancel()
	<-done
}

func TestRunBuildRuntimeFailsOnUnknownPool(t *testing.T) {
	opts := baseOpts(t, "epsilon")
	// Rewrite agent.yaml with an unknown pool.
	yml := `schema_version: "1"
name: epsilon
description: test agent
backend:
  type: cli
  command: ["/bin/echo"]
execution:
  mode: service
  pool: nonexistent
conversation:
  mode: stateless
triggers:
  - type: cron
    schedule: "0 * * * *"
tools: []
`
	if err := os.WriteFile(filepath.Join(opts.AgentDir, "agent.yaml"), []byte(yml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	code, err := Run(context.Background(), opts)
	if code != ExitBuildRuntime {
		t.Errorf("code = %d, want %d", code, ExitBuildRuntime)
	}
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestLoadConfigMissingPathFallsBackToDefault(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(shortTempDir(t), "nope.yaml"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg == nil {
		t.Fatalf("cfg is nil")
	}
}

func TestRunReloadAfterEdit(t *testing.T) {
	opts := baseOpts(t, "gamma")
	opts.Version = "1.0.0"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		code, _ := Run(ctx, opts)
		done <- code
	}()

	sockPath := filepath.Join(opts.RunDir, opts.AgentName+".sock")
	c := dialSocket(t, sockPath)
	defer c.Close()
	sc := bufio.NewScanner(c)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, 1<<20)

	sendJSON(t, c, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "handshake", "params": map[string]any{"version": "1.0.0"}})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_ = readJSON(t, sc)

	// Corrupt the agent.yaml so reload fails.
	if err := os.WriteFile(filepath.Join(opts.AgentDir, "agent.yaml"), []byte("{ not yaml ["), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sendJSON(t, c, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "reload"})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp := readJSON(t, sc)
	if resp["error"] == nil {
		t.Fatalf("expected reload error, got %+v", resp["result"])
	}

	// Restore and call reload again — should succeed.
	if err := os.WriteFile(filepath.Join(opts.AgentDir, "agent.yaml"), []byte(fmt.Sprintf(validAgentYAML, "gamma")), 0o600); err != nil {
		t.Fatalf("restore: %v", err)
	}
	sendJSON(t, c, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "reload"})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp = readJSON(t, sc)
	if resp["error"] != nil {
		t.Fatalf("reload err: %+v", resp["error"])
	}

	cancel()
	<-done
}
