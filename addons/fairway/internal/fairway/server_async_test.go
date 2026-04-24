package fairway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// ── test doubles ──────────────────────────────────────────────────────────────

// asyncFakeExecutor records Execute calls, sleeps for delay, and signals
// completion by closing the done channel. The body of the request is captured
// so tests can assert body propagation into the detached goroutine.
type asyncFakeExecutor struct {
	delay  time.Duration
	result fairway.Result

	mu        sync.Mutex
	called    atomic.Int64
	doneCh    chan struct{}
	lastBody  []byte
	lastRoute fairway.Route
}

func newAsyncFakeExecutor(delay time.Duration, result fairway.Result) *asyncFakeExecutor {
	return &asyncFakeExecutor{
		delay:  delay,
		result: result,
		doneCh: make(chan struct{}),
	}
}

func (f *asyncFakeExecutor) Execute(ctx context.Context, route fairway.Route, r *http.Request) (fairway.Result, error) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.lastBody = body
	f.lastRoute = route
	f.mu.Unlock()

	f.called.Add(1)

	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		// Keep the result stable on context cancellation — the point of the
		// async branch is that cancellation of the HTTP request context does
		// NOT reach here; the goroutine uses a detached context.
		close(f.doneCh)
		return fairway.Result{HTTPStatus: 504}, nil
	}

	close(f.doneCh)
	return f.result, nil
}

func (f *asyncFakeExecutor) waitDone(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-f.doneCh:
	case <-time.After(timeout):
		t.Fatalf("async executor did not complete within %s", timeout)
	}
}

func (f *asyncFakeExecutor) body() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.lastBody...)
}

// ── async response: 202 + X-Trace-Id ─────────────────────────────────────────

func TestServeHTTP_async_respondsFastWith202(t *testing.T) {
	t.Parallel()

	exec := newAsyncFakeExecutor(500*time.Millisecond, fairway.Result{HTTPStatus: 200, Body: []byte("done")})
	route := fairway.Route{
		Path:   "/async",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCrewRun, Target: "agent-x"},
		Async:  true,
	}
	srv := buildServer(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	start := time.Now()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/async", strings.NewReader(`{"ping":"pong"}`))
	r.RemoteAddr = "127.0.0.1:12345"
	handler.ServeHTTP(w, r)
	ackElapsed := time.Since(start)

	if w.Code != http.StatusAccepted {
		t.Fatalf("async ack status = %d; want 202", w.Code)
	}
	if ackElapsed > 100*time.Millisecond {
		t.Fatalf("async ack took %s; want < 100ms (executor delay is 500ms)", ackElapsed)
	}

	trace := w.Header().Get("X-Trace-Id")
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(trace) {
		t.Errorf("X-Trace-Id = %q; want 16 lowercase hex chars", trace)
	}

	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("async response body is not JSON: %v (body=%s)", err, w.Body.String())
	}
	if body["status"] != "accepted" {
		t.Errorf("body.status = %q; want \"accepted\"", body["status"])
	}
	if body["trace_id"] != trace {
		t.Errorf("body.trace_id = %q; want header trace %q", body["trace_id"], trace)
	}

	// Goroutine must eventually run and finish.
	exec.waitDone(t, 2*time.Second)
	if exec.called.Load() != 1 {
		t.Errorf("executor called %d times; want 1", exec.called.Load())
	}
}

// ── body propagation: stdin body survives into detached goroutine ────────────

func TestServeHTTP_async_bodyReachesExecutor(t *testing.T) {
	t.Parallel()

	exec := newAsyncFakeExecutor(50*time.Millisecond, fairway.Result{HTTPStatus: 200})
	route := fairway.Route{
		Path:   "/async",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCrewRun, Target: "agent"},
		Async:  true,
	}
	srv := buildServer(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	payload := `{"action":"opened","issue":{"number":42}}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/async", strings.NewReader(payload))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	exec.waitDone(t, 2*time.Second)

	if got := string(exec.body()); got != payload {
		t.Errorf("executor body = %q; want %q", got, payload)
	}
}

// ── client-cancel safety: detached goroutine outlives the request context ────

func TestServeHTTP_async_clientCancel_taskSurvives(t *testing.T) {
	t.Parallel()

	// Executor delay longer than the request context lifetime.
	exec := newAsyncFakeExecutor(300*time.Millisecond, fairway.Result{HTTPStatus: 200})
	route := fairway.Route{
		Path:   "/async",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCrewRun, Target: "agent"},
		Async:  true,
	}
	srv := buildServer(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	// Build a request whose context has already been cancelled at the moment
	// of dispatch. If the async goroutine were attached to r.Context(), it
	// would abort immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/async", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("async ack status = %d; want 202", w.Code)
	}

	exec.waitDone(t, 2*time.Second)
	if exec.called.Load() != 1 {
		t.Errorf("executor called %d times; want 1 despite cancelled request context", exec.called.Load())
	}
}

// ── sync parity: non-async route does not get the async treatment ────────────

func TestServeHTTP_sync_noTraceIDHeader(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: []byte("ok")}}
	route := fairway.Route{
		Path:   "/sync",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	srv := buildServer(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/sync", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	if w.Code != 200 {
		t.Fatalf("sync status = %d; want 200", w.Code)
	}
	if trace := w.Header().Get("X-Trace-Id"); trace != "" {
		t.Errorf("sync route must not set X-Trace-Id; got %q", trace)
	}
}

// ── observation: async logs one entry with real ExitCode and trace_id ────────

func TestServeHTTP_async_observeCarriesFinalExitCodeAndTraceID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fixed := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	rl, err := fairway.NewRequestLogger(dir, func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer rl.Close() //nolint:errcheck

	exec := newAsyncFakeExecutor(30*time.Millisecond, fairway.Result{HTTPStatus: 200, ExitCode: 0})
	route := fairway.Route{
		Path:   "/async-log",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCrewRun, Target: "agent"},
		Async:  true,
	}

	srv := buildServerWithLogger(t, exec, rl, nil, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/async-log", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("async ack status = %d; want 202", w.Code)
	}
	wantTrace := w.Header().Get("X-Trace-Id")

	exec.waitDone(t, 2*time.Second)

	// Give the observer goroutine a beat to flush the log line.
	time.Sleep(50 * time.Millisecond)
	_ = rl.Close()

	logFile := filepath.Join(dir, "2026-04-23.jsonl")
	f, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var events []fairway.RequestEvent
	for scanner.Scan() {
		var evt fairway.RequestEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		events = append(events, evt)
	}

	if len(events) != 1 {
		t.Fatalf("observer wrote %d events for async request; want exactly 1", len(events))
	}
	evt := events[0]
	if evt.Data.Status != http.StatusAccepted {
		t.Errorf("Data.Status = %d; want 202 (what the client actually received)", evt.Data.Status)
	}
	if evt.Data.ExitCode != 0 {
		t.Errorf("Data.ExitCode = %d; want 0 (real action exit code)", evt.Data.ExitCode)
	}
	if evt.Data.TraceID == "" {
		t.Errorf("Data.TraceID empty; want matching ack header %q", wantTrace)
	}
	if evt.Data.TraceID != wantTrace {
		t.Errorf("Data.TraceID = %q; want %q (ack header)", evt.Data.TraceID, wantTrace)
	}
}

// ── graceful shutdown waits for async goroutines ─────────────────────────────

func TestServe_gracefulShutdownWaitsAsyncGoroutines(t *testing.T) {
	t.Parallel()

	// Executor takes 300ms; we cancel the server context at 50ms. If the
	// shutdown did not wait for async goroutines, the test would observe a
	// partial execution state (completed < 1) right after Serve returns.
	exec := newAsyncFakeExecutor(300*time.Millisecond, fairway.Result{HTTPStatus: 200})
	route := fairway.Route{
		Path:   "/async",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCrewRun, Target: "agent"},
		Async:  true,
	}

	srv := buildServerWithRoutesOnEphemeralPort(t, exec, route)

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()
	waitForServer(t, srv, 2*time.Second)

	// Fire the async request.
	resp, err := http.Post("http://"+srv.Addr()+"/async", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("async ack status = %d; want 202", resp.StatusCode)
	}

	// Shortly after dispatch, cancel the server — async work must still complete.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-serveDone:
		if err != nil {
			t.Errorf("Serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within 5s after cancel")
	}

	// When Serve returns, shutdown should have drained the async goroutine.
	if exec.called.Load() != 1 {
		t.Errorf("executor called %d times; want 1", exec.called.Load())
	}
	select {
	case <-exec.doneCh:
	case <-time.After(1 * time.Second):
		t.Fatal("async goroutine did not complete before Serve returned (shutdown raced)")
	}
}

// buildServerWithRoutesOnEphemeralPort builds a Server bound to a free ephemeral port,
// with the given routes configured. Complements buildServer (handler-only) and
// buildServerOnPort (no routes), which don't accept both.
func buildServerWithRoutesOnEphemeralPort(t *testing.T, exec fairway.Executor, routes ...fairway.Route) *fairway.Server {
	t.Helper()
	cfg := baseConfig()
	cfg.Routes = routes
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(lis.Addr().String())
	fmt.Sscanf(portStr, "%d", &cfg.Port)
	lis.Close()

	repo := &fakeRepo{cfg: cfg}
	router := fairway.NewRouterWithConfig(repo, cfg)
	return fairway.NewServer(fairway.ServerConfig{Router: router, Executor: exec})
}
