package socket

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
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu      sync.Mutex
	calls   int
	delay   time.Duration
	err     error
	result  RunResult
	lastCtx context.Context
	lastIn  RunParams
}

func (r *fakeRunner) Run(ctx context.Context, p RunParams) (RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.lastCtx = ctx
	r.lastIn = p
	delay := r.delay
	err := r.err
	res := r.result
	r.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		}
	}
	return res, err
}

func newTestServer(t *testing.T, deps Deps) (*Server, string) {
	t.Helper()
	// macOS limits Unix socket paths to 104 bytes. t.TempDir() can exceed
	// that. Use a short path under /tmp and clean up explicitly.
	dir, err := os.MkdirTemp("/tmp", "crew")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	if deps.Version == "" {
		deps.Version = "1.2.3"
	}
	if deps.AgentName == "" {
		deps.AgentName = "test"
	}
	srv, err := NewServer(path, deps)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv, path
}

func startServe(t *testing.T, srv *Server) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	// Wait briefly until listener is accepting.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if srv.Addr() != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return cancel, done
}

type testClient struct {
	conn net.Conn
	sc   *bufio.Scanner
}

func dial(t *testing.T, path string) *testClient {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sc := bufio.NewScanner(c)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, MaxMessageSize)
	return &testClient{conn: c, sc: sc}
}

func (c *testClient) send(id int, method string, params any) {
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, _ := json.Marshal(req)
	b = append(b, '\n')
	if _, err := c.conn.Write(b); err != nil {
		panic(err)
	}
}

func (c *testClient) recv(t *testing.T) Response {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !c.sc.Scan() {
		t.Fatalf("recv scan failed: %v", c.sc.Err())
	}
	var resp Response
	if err := json.Unmarshal(c.sc.Bytes(), &resp); err != nil {
		t.Fatalf("recv unmarshal: %v; line=%s", err, c.sc.Text())
	}
	return resp
}

func (c *testClient) close() { _ = c.conn.Close() }

func handshakeOK(t *testing.T, c *testClient, version string) {
	t.Helper()
	c.send(1, "handshake", map[string]any{"version": version})
	resp := c.recv(t)
	if resp.Error != nil {
		t.Fatalf("unexpected handshake error: %+v", resp.Error)
	}
}

// ── Unit: RPC serialization ──────────────────────────────────────────────────

func TestRequestResponseRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"with id", `{"jsonrpc":"2.0","id":7,"method":"ping","params":{"x":1}}`},
		{"string id", `{"jsonrpc":"2.0","id":"abc","method":"ping"}`},
		{"no params", `{"jsonrpc":"2.0","id":1,"method":"ping"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req Request
			if err := json.Unmarshal([]byte(tc.json), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if req.Method != "ping" {
				t.Errorf("method = %q, want ping", req.Method)
			}
		})
	}
}

func TestErrorCodes(t *testing.T) {
	cases := map[string]int{
		"parse":  ErrCodeParseError,
		"invreq": ErrCodeInvalidRequest,
		"nomth":  ErrCodeMethodNotFound,
		"invpar": ErrCodeInvalidParams,
		"intern": ErrCodeInternal,
		"vermm":  ErrCodeVersionMismatch,
		"app":    ErrCodeAppSpecific,
	}
	want := map[string]int{
		"parse":  -32700,
		"invreq": -32600,
		"nomth":  -32601,
		"invpar": -32602,
		"intern": -32603,
		"vermm":  -32010,
		"app":    -32000,
	}
	for k, v := range cases {
		if want[k] != v {
			t.Errorf("%s = %d, want %d", k, v, want[k])
		}
	}
}

func TestCompatibleVersion(t *testing.T) {
	cases := []struct {
		a, b string
		ok   bool
	}{
		{"1.2.3", "1.9.0", true},
		{"1.0.0", "1.0.0", true},
		{"2.0.0", "1.9.9", false},
		{"dev", "dev", true},
		{"dev", "1.0.0", false},
		{"1.0.0", "dev", false},
		{"v1.2", "1.0", true},
	}
	for _, tc := range cases {
		if got := compatibleVersion(tc.a, tc.b); got != tc.ok {
			t.Errorf("compatibleVersion(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.ok)
		}
	}
}

// ── Integration ──────────────────────────────────────────────────────────────

func TestSocketPermissionIs0600(t *testing.T) {
	srv, path := newTestServer(t, Deps{})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("socket mode = %o, want 0600", mode)
	}
}

func TestHandshakeOK(t *testing.T) {
	srv, path := newTestServer(t, Deps{Version: "1.4.0", AgentName: "billy"})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()

	c.send(1, "handshake", map[string]any{"version": "1.9.0"})
	resp := c.recv(t)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is not a map: %T", resp.Result)
	}
	if m["agent"] != "billy" {
		t.Errorf("agent = %v, want billy", m["agent"])
	}
	if m["version"] != "1.4.0" {
		t.Errorf("version = %v, want 1.4.0", m["version"])
	}
}

func TestHandshakeVersionMismatchClosesConn(t *testing.T) {
	srv, path := newTestServer(t, Deps{Version: "1.4.0"})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()

	c.send(1, "handshake", map[string]any{"version": "2.0.0"})
	resp := c.recv(t)
	if resp.Error == nil {
		t.Fatalf("expected error, got result: %+v", resp.Result)
	}
	if resp.Error.Code != ErrCodeVersionMismatch {
		t.Errorf("code = %d, want %d", resp.Error.Code, ErrCodeVersionMismatch)
	}

	// Second read should observe EOF (conn closed).
	_ = c.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := c.conn.Read(buf)
	if err == nil {
		t.Fatalf("expected conn closed, got read ok")
	}
}

func TestHandshakeTimeoutClosesConn(t *testing.T) {
	srv, path := newTestServer(t, Deps{HandshakeTimeout: 100 * time.Millisecond})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()

	// Send nothing. The server should close the connection after the timeout.
	_ = c.conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	_, err := c.conn.Read(buf)
	if err == nil {
		t.Fatalf("expected conn closed, got read ok")
	}
}

func TestFirstMessageMustBeHandshake(t *testing.T) {
	srv, path := newTestServer(t, Deps{})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()

	c.send(1, "status", nil)
	resp := c.recv(t)
	if resp.Error == nil {
		t.Fatalf("expected error, got result: %+v", resp.Result)
	}
	if resp.Error.Code != ErrCodeInvalidRequest {
		t.Errorf("code = %d, want %d", resp.Error.Code, ErrCodeInvalidRequest)
	}
}

func TestMethodNotFound(t *testing.T) {
	srv, path := newTestServer(t, Deps{})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()
	handshakeOK(t, c, "1.2.3")

	c.send(2, "nope", nil)
	resp := c.recv(t)
	if resp.Error == nil || resp.Error.Code != ErrCodeMethodNotFound {
		t.Fatalf("want method-not-found, got %+v / %+v", resp.Result, resp.Error)
	}
}

func TestRunInvalidParams(t *testing.T) {
	r := &fakeRunner{}
	srv, path := newTestServer(t, Deps{Runner: r})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()
	handshakeOK(t, c, "1.2.3")

	// Send non-JSON-object params for run — but since params "input" accepts any,
	// test an invalid payload by sending a non-object as the whole params slot.
	// Here we send params as a primitive which fails to decode into the struct.
	req := `{"jsonrpc":"2.0","id":3,"method":"run","params":"not-an-object"}` + "\n"
	if _, err := c.conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := c.recv(t)
	if resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("want invalid-params, got %+v / %+v", resp.Result, resp.Error)
	}
}

func TestRunSuccess(t *testing.T) {
	r := &fakeRunner{result: RunResult{TraceID: "tid-1", Text: "hello", Data: map[string]any{"k": 1.0}}}
	srv, path := newTestServer(t, Deps{Runner: r})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()
	handshakeOK(t, c, "1.2.3")

	c.send(3, "run", map[string]any{"input": map[string]any{"user": "hi"}, "timeout_ms": 500})
	resp := c.recv(t)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["trace_id"] != "tid-1" {
		t.Errorf("trace_id = %v", m["trace_id"])
	}
	if m["status"] != "ok" {
		t.Errorf("status = %v", m["status"])
	}
	out := m["output"].(map[string]any)
	if out["text"] != "hello" {
		t.Errorf("text = %v", out["text"])
	}

	if r.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", r.calls)
	}
	if r.lastIn.Input["user"] != "hi" {
		t.Errorf("runner received input = %+v", r.lastIn.Input)
	}
	if r.lastIn.TimeoutMs != 500 {
		t.Errorf("runner received timeout_ms = %d", r.lastIn.TimeoutMs)
	}
}

func TestRunRunnerError(t *testing.T) {
	r := &fakeRunner{err: errors.New("boom"), result: RunResult{TraceID: "t-x"}}
	srv, path := newTestServer(t, Deps{Runner: r})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()
	handshakeOK(t, c, "1.2.3")

	c.send(4, "run", map[string]any{"input": map[string]any{}})
	resp := c.recv(t)
	if resp.Error == nil || resp.Error.Code != ErrCodeAppSpecific {
		t.Fatalf("want app-specific error, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "boom") {
		t.Errorf("error message = %q", resp.Error.Message)
	}
}

func TestStatus(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := &atomicClock{t: now}
	srv, path := newTestServer(t, Deps{AgentName: "alpha", Version: "1.0.0", Now: clock.Now})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()
	handshakeOK(t, c, "1.0.0")

	clock.Advance(42 * time.Second)
	c.send(5, "status", nil)
	resp := c.recv(t)
	if resp.Error != nil {
		t.Fatalf("status error: %+v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["agent"] != "alpha" {
		t.Errorf("agent = %v", m["agent"])
	}
	if m["version"] != "1.0.0" {
		t.Errorf("version = %v", m["version"])
	}
	if fmt.Sprintf("%v", m["uptime_seconds"]) != "42" {
		t.Errorf("uptime_seconds = %v", m["uptime_seconds"])
	}
}

func TestReload(t *testing.T) {
	called := 0
	reloadErr := errors.New("broken")
	mode := "ok"
	srv, path := newTestServer(t, Deps{Reload: func(context.Context) error {
		called++
		if mode == "err" {
			return reloadErr
		}
		return nil
	}})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()
	handshakeOK(t, c, "1.2.3")

	c.send(6, "reload", nil)
	resp := c.recv(t)
	if resp.Error != nil {
		t.Fatalf("reload err: %+v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["reloaded"] != true {
		t.Errorf("reloaded = %v", m["reloaded"])
	}

	mode = "err"
	c.send(7, "reload", nil)
	resp = c.recv(t)
	if resp.Error == nil || resp.Error.Code != ErrCodeInternal {
		t.Fatalf("want internal error, got %+v", resp.Error)
	}
	if called != 2 {
		t.Errorf("reload called = %d, want 2", called)
	}
}

func TestShutdownMethod(t *testing.T) {
	signalled := make(chan struct{}, 1)
	srv, path := newTestServer(t, Deps{OnShutdown: func() { signalled <- struct{}{} }})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()
	handshakeOK(t, c, "1.2.3")

	c.send(8, "shutdown", nil)
	resp := c.recv(t)
	if resp.Error != nil {
		t.Fatalf("shutdown err: %+v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["status"] != "shutting_down" {
		t.Errorf("status = %v", m["status"])
	}
	select {
	case <-signalled:
	case <-time.After(time.Second):
		t.Fatalf("OnShutdown not called")
	}
}

func TestConcurrentConnections(t *testing.T) {
	r := &fakeRunner{result: RunResult{TraceID: "t", Text: "ok"}}
	srv, path := newTestServer(t, Deps{Runner: r})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	var wg sync.WaitGroup
	const N = 10
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			c := dial(t, path)
			defer c.close()
			handshakeOK(t, c, "1.2.3")
			c.send(i+100, "run", map[string]any{"input": map[string]any{}})
			resp := c.recv(t)
			if resp.Error != nil {
				t.Errorf("conn %d err: %+v", i, resp.Error)
			}
		}()
	}
	wg.Wait()
	if r.calls != N {
		t.Errorf("runner calls = %d, want %d", r.calls, N)
	}
}

func TestShutdownReleasesServe(t *testing.T) {
	srv, _ := newTestServer(t, Deps{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	time.Sleep(20 * time.Millisecond)
	shutdownCtx, shCancel := context.WithTimeout(context.Background(), time.Second)
	defer shCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Serve did not return")
	}
}

func TestShutdownTimeoutOnHangingConn(t *testing.T) {
	r := &fakeRunner{delay: 2 * time.Second, result: RunResult{TraceID: "t"}}
	srv, path := newTestServer(t, Deps{Runner: r})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()
	time.Sleep(20 * time.Millisecond)

	c := dial(t, path)
	defer c.close()
	handshakeOK(t, c, "1.2.3")
	c.send(1, "run", map[string]any{"input": map[string]any{}})

	// Give the server a moment to start the run.
	time.Sleep(50 * time.Millisecond)

	shCtx, shCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shCancel()
	err := srv.Shutdown(shCtx)
	if err == nil {
		t.Fatalf("expected timeout error")
	}

	// Cleanup.
	cancel()
	<-serveDone
}

func TestTooLargeMessage(t *testing.T) {
	srv, path := newTestServer(t, Deps{})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()
	handshakeOK(t, c, "1.2.3")

	big := strings.Repeat("x", MaxMessageSize+10)
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"run","params":{"input":{"k":%q}}}`, big)
	// Write asynchronously: the unix socket pipe buffer is small, so a
	// large blocking write would deadlock against the server reader/error
	// response on this same goroutine.
	go func() { _, _ = c.conn.Write([]byte(req + "\n")) }()
	resp := c.recv(t)
	if resp.Error == nil || resp.Error.Code != ErrCodeInvalidRequest {
		t.Fatalf("want invalid-request, got %+v", resp.Error)
	}
}

func TestParseErrorOnMalformedJSON(t *testing.T) {
	srv, path := newTestServer(t, Deps{})
	cancel, done := startServe(t, srv)
	defer func() {
		cancel()
		<-done
	}()

	c := dial(t, path)
	defer c.close()
	handshakeOK(t, c, "1.2.3")

	if _, err := c.conn.Write([]byte("{not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := c.recv(t)
	if resp.Error == nil || resp.Error.Code != ErrCodeParseError {
		t.Fatalf("want parse-error, got %+v", resp.Error)
	}
}

func TestNewServerRequiresPathAndVersion(t *testing.T) {
	if _, err := NewServer("", Deps{Version: "1.0.0"}); err == nil {
		t.Errorf("expected error for empty path")
	}
	dir, err := os.MkdirTemp("/tmp", "crew")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)
	if _, err := NewServer(filepath.Join(dir, "s.sock"), Deps{}); err == nil {
		t.Errorf("expected error for empty version")
	}
}

// atomicClock is a thread-safe injectable clock.
type atomicClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *atomicClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *atomicClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
