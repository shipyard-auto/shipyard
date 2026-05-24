package fairway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
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

// ── sync parity: every response carries a trace id from the middleware ───────

func TestServeHTTP_sync_carriesTraceIDHeader(t *testing.T) {
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
	if trace := w.Header().Get("X-Trace-Id"); trace == "" {
		t.Error("sync route must echo X-Trace-Id from the middleware; got empty value")
	}
}

// Legacy v1 async observe test removed — equivalent schema-v2 coverage
// lives in server_event_logger_test.go.

// ── F-01: pool gate backpressure on async ack ───────────────────────────────

// countingPoolExecutor implements PoolGate with controllable Acquire/Execute
// behavior so we can assert server-side decisions independently from the
// real pool mechanics. Mock targeted at server-level F-01 invariants;
// real-executor coverage lives in TestServeHTTP_async_poolFull_returns503.
type countingPoolExecutor struct {
	mu            sync.Mutex
	acquireCalls  int
	acquireRefuse bool // when true, Acquire returns ok=false
	releaseCalled bool
	executeCalls  int
	executeNoPool int
	executeResult fairway.Result
}

func (c *countingPoolExecutor) Acquire(_ context.Context) (release func(), ok bool) {
	c.mu.Lock()
	c.acquireCalls++
	refuse := c.acquireRefuse
	c.mu.Unlock()
	if refuse {
		return nil, false
	}
	return func() {
		c.mu.Lock()
		c.releaseCalled = true
		c.mu.Unlock()
	}, true
}

func (c *countingPoolExecutor) Execute(_ context.Context, _ fairway.Route, _ *http.Request) (fairway.Result, error) {
	c.mu.Lock()
	c.executeCalls++
	c.mu.Unlock()
	return c.executeResult, nil
}

func (c *countingPoolExecutor) ExecuteWithoutPool(_ context.Context, _ fairway.Route, _ *http.Request) (fairway.Result, error) {
	c.mu.Lock()
	c.executeNoPool++
	c.mu.Unlock()
	return c.executeResult, nil
}

// TestServeHTTP_async_poolRefused_returns503_withRetryAfter asserts the
// server refuses the request with 503 + Retry-After when PoolGate.Acquire
// returns ok=false. Without F-01 the client would have received a lying
// 202 and the work would have been dropped silently.
func TestServeHTTP_async_poolRefused_returns503_withRetryAfter(t *testing.T) {
	t.Parallel()

	exec := &countingPoolExecutor{acquireRefuse: true}
	route := fairway.Route{
		Path:    "/async",
		Async:   true,
		Auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
		Action:  fairway.Action{Type: fairway.ActionCrewRun, Target: "agent"},
		Timeout: time.Second,
	}
	srv := buildServer(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/async", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Error("Retry-After header missing on 503; client has no signal to back off")
	}
	if exec.executeCalls != 0 || exec.executeNoPool != 0 {
		t.Errorf("executor must NOT run when pool refuses: Execute=%d ExecuteWithoutPool=%d",
			exec.executeCalls, exec.executeNoPool)
	}
	// The X-Trace-Id should still echo (operator may want to grep the log
	// even for refusals).
	if trace := w.Header().Get("X-Trace-Id"); trace == "" {
		t.Error("X-Trace-Id missing on 503 response")
	}
}

// TestServeHTTP_async_poolAcquired_releasedAfterExecute asserts the
// server uses ExecuteWithoutPool (not Execute) when it pre-acquired a
// slot, and that the release function runs exactly once after the
// goroutine finishes — otherwise we'd leak slots indefinitely.
func TestServeHTTP_async_poolAcquired_releasedAfterExecute(t *testing.T) {
	t.Parallel()

	exec := &countingPoolExecutor{
		executeResult: fairway.Result{HTTPStatus: 200, Body: []byte("ok")},
	}
	route := fairway.Route{
		Path:    "/async",
		Async:   true,
		Auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
		Action:  fairway.Action{Type: fairway.ActionCrewRun, Target: "agent"},
		Timeout: time.Second,
	}
	srv := buildServer(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/async", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	// Wait for the detached goroutine to finish.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		exec.mu.Lock()
		done := exec.releaseCalled
		exec.mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	exec.mu.Lock()
	defer exec.mu.Unlock()
	if exec.acquireCalls != 1 {
		t.Errorf("Acquire called %d times, want 1", exec.acquireCalls)
	}
	if exec.executeNoPool != 1 {
		t.Errorf("ExecuteWithoutPool called %d times, want 1 (server must skip pool re-entry)", exec.executeNoPool)
	}
	if exec.executeCalls != 0 {
		t.Errorf("Execute (pool-acquiring path) called %d times, want 0; server must use ExecuteWithoutPool", exec.executeCalls)
	}
	if !exec.releaseCalled {
		t.Error("release() never called — pool slot would leak forever")
	}
}

// TestServeHTTP_async_httpForward_doesNotAcquirePool guards the carve-out:
// http.forward async routes don't use the subprocess pool, so PoolGate.
// Acquire must NOT be invoked. Otherwise a saturated subprocess pool
// would block fire-and-forget webhook notifications that have nothing to
// do with subprocesses.
func TestServeHTTP_async_httpForward_doesNotAcquirePool(t *testing.T) {
	t.Parallel()

	exec := &countingPoolExecutor{
		executeResult: fairway.Result{HTTPStatus: 200, Body: []byte("ok")},
	}
	route := fairway.Route{
		Path:    "/notify",
		Async:   true,
		Auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
		Action:  fairway.Action{Type: fairway.ActionHTTPForward, URL: "https://example.com"},
		Timeout: time.Second,
	}
	srv := buildServer(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	// Wait briefly for goroutine to complete the executor call.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		exec.mu.Lock()
		done := exec.executeCalls + exec.executeNoPool
		exec.mu.Unlock()
		if done > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	exec.mu.Lock()
	defer exec.mu.Unlock()
	if exec.acquireCalls != 0 {
		t.Errorf("Acquire called %d times for http.forward async; want 0 (forward bypasses pool)", exec.acquireCalls)
	}
}

// TestServeHTTP_async_poolFull_returns503 is the end-to-end version using
// the real production executor with MaxInFlight=1 and a real (slow)
// subprocess. Proves the F-01 fix works when wired against the actual
// pool implementation, not just against a mock — guards against the mock
// drifting from real semantics.
func TestServeHTTP_async_poolFull_returns503(t *testing.T) {
	t.Parallel()

	// Real executor: 1 slot, 100ms queue timeout, sleep-based subprocess
	// to keep the slot busy long enough for the second request to be
	// refused.
	realExec := fairway.NewExecutor(fairway.ExecutorConfig{
		MaxInFlight:    1,
		QueueTimeout:   100 * time.Millisecond,
		DefaultTimeout: 2 * time.Second,
		Run: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sleep", "1")
		},
	})
	route := fairway.Route{
		Path:    "/async",
		Async:   true,
		Auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
		Action:  fairway.Action{Type: fairway.ActionCrewRun, Target: "agent"},
		Timeout: 2 * time.Second,
	}
	srv := buildServer(t, realExec, route)
	handler := fairway.ServerHandlerForTest(srv)

	// First request occupies the only slot.
	first := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodPost, "/async", strings.NewReader("{}"))
	r1.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(first, r1)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first request = %d, want 202", first.Code)
	}

	// Give the goroutine a moment to start the subprocess (acquire the slot).
	time.Sleep(50 * time.Millisecond)

	// Second request finds the pool full. With F-01 the server refuses
	// with 503 instead of writing a lying 202.
	second := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodPost, "/async", strings.NewReader("{}"))
	r2.RemoteAddr = "127.0.0.1:2"
	handler.ServeHTTP(second, r2)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second request = %d, want 503 (pool was full)", second.Code)
	}
	if ra := second.Header().Get("Retry-After"); ra == "" {
		t.Error("Retry-After header missing on real-executor 503")
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
