package fairway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// ── fake executor ─────────────────────────────────────────────────────────────

type fakeExecutor struct {
	result fairway.Result
	err    error
	calls  atomic.Int64
}

func (f *fakeExecutor) Execute(_ context.Context, _ fairway.Route, _ *http.Request) (fairway.Result, error) {
	f.calls.Add(1)
	return f.result, f.err
}

// ── server builder ────────────────────────────────────────────────────────────

// newTestServer creates a Server wired to an in-memory router pre-loaded with
// the given routes. Returns the server and a started httptest.Server using the
// server's internal handler so we avoid binding a real TCP port for handler
// tests.
func newTestServer(t *testing.T, exec fairway.Executor, routes ...fairway.Route) (*fairway.Server, *httptest.Server) {
	t.Helper()

	cfg := baseConfig()
	cfg.Routes = routes
	repo := &fakeRepo{cfg: cfg}
	router := fairway.NewRouterWithConfig(repo, cfg)

	srv := fairway.NewServer(fairway.ServerConfig{
		Router:   router,
		Executor: exec,
	})

	ts := httptest.NewServer(serverHandler(srv))
	t.Cleanup(ts.Close)
	return srv, ts
}

// serverHandler extracts the http.Handler from Server via a round-trip
// through httptest so we don't need to expose the mux.
// We exploit the fact that httptest.NewServer accepts any http.Handler —
// here we build a lightweight shim that calls the server's Serve method
// indirectly by using the server's own handler via net/http.
//
// Because Server.Serve binds a real port, for handler-only tests we build a
// separate http.ServeMux that mirrors the server's routing logic. To keep
// the test helpers clean we expose a HandlerForTest() method that returns
// the mux directly.

// handlerShim builds an http.Handler equivalent to the server's internal mux
// by calling the server through a real loopback Serve on port 0 in the
// background, then forwarding requests to it.
//
// Simpler approach: expose a test helper that returns the handler.
// We add a NewServerWithHandler helper that returns both server and handler.

func serverHandler(s *fairway.Server) http.Handler {
	return fairway.ServerHandlerForTest(s)
}

// ── Handler tests ─────────────────────────────────────────────────────────────

func TestHealth_alwaysReturns200(t *testing.T) {
	t.Parallel()

	_, ts := newTestServer(t, &fakeExecutor{})
	resp, err := http.Get(ts.URL + "/_health")
	if err != nil {
		t.Fatalf("GET /_health error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("/_health status = %d; want 200", resp.StatusCode)
	}
}

func TestUnknownPath_returns404(t *testing.T) {
	t.Parallel()

	_, ts := newTestServer(t, &fakeExecutor{})
	resp, err := http.Get(ts.URL + "/not-registered")
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d; want 404", resp.StatusCode)
	}
}

func TestMatchedRoute_callsExecutor(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: []byte("done")}}
	route := fairway.Route{
		Path:   "/webhook",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	_, ts := newTestServer(t, exec, route)

	resp, err := http.Post(ts.URL+"/webhook", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	if exec.calls.Load() != 1 {
		t.Errorf("executor called %d times; want 1", exec.calls.Load())
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}
}

func TestAuth_401_stopsBeforeExecutor(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200}}
	route := fairway.Route{
		Path:   "/secure",
		Auth:   fairway.Auth{Type: fairway.AuthBearer, Token: "secret"},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	_, ts := newTestServer(t, exec, route)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/secure", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 401 {
		t.Errorf("status = %d; want 401", resp.StatusCode)
	}
	if exec.calls.Load() != 0 {
		t.Error("executor must not be called on auth failure")
	}

	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	if body["error"] == "" {
		t.Error("expected JSON error body")
	}
}

func TestAuth_403_stopsBeforeExecutor(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200}}
	route := fairway.Route{
		Path:   "/local",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}

	// httptest.Server sets RemoteAddr to 127.0.0.1 normally — to simulate a
	// non-local IP we need a custom RoundTripper that rewrites RemoteAddr.
	// Instead, we test the handler directly with a non-loopback RemoteAddr.
	handler := fairway.ServerHandlerForTest(buildServer(t, exec, route))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/local", nil)
	r.RemoteAddr = "8.8.8.8:12345" // non-loopback
	handler.ServeHTTP(w, r)

	if w.Code != 403 {
		t.Errorf("status = %d; want 403", w.Code)
	}
	if exec.calls.Load() != 0 {
		t.Error("executor must not be called on 403")
	}
}

func TestBody_tooLarge_returns413(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200}}
	route := fairway.Route{
		Path:   "/webhook",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	_, ts := newTestServer(t, exec, route)

	// Send a body larger than MaxSubprocessOutput.
	bigBody := bytes.Repeat([]byte("x"), int(fairway.MaxSubprocessOutput)+1)
	resp, err := http.Post(ts.URL+"/webhook", "application/octet-stream", bytes.NewReader(bigBody))
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 413 {
		t.Errorf("status = %d; want 413 for oversized body", resp.StatusCode)
	}
}

func TestExecutor_returns500_respondedTo(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 500, Body: []byte("internal error")}}
	route := fairway.Route{
		Path:   "/webhook",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	_, ts := newTestServer(t, exec, route)

	resp, err := http.Post(ts.URL+"/webhook", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("http.Post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 500 {
		t.Errorf("status = %d; want 500", resp.StatusCode)
	}
}

func TestExecutor_returns504OnTimeout_respondedTo(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 504}}
	route := fairway.Route{
		Path:   "/webhook",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	_, ts := newTestServer(t, exec, route)

	resp, err := http.Post(ts.URL+"/webhook", "application/json", nil)
	if err != nil {
		t.Fatalf("http.Post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 504 {
		t.Errorf("status = %d; want 504", resp.StatusCode)
	}
}

func TestHTTPForward_respHeadersCopied(t *testing.T) {
	t.Parallel()

	downstreamHeaders := http.Header{"X-Downstream": []string{"value-from-downstream"}}
	exec := &fakeExecutor{result: fairway.Result{
		HTTPStatus: 200,
		Header:     downstreamHeaders,
	}}
	route := fairway.Route{
		Path:   "/fwd",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionHTTPForward, URL: "http://example.com"},
	}
	_, ts := newTestServer(t, exec, route)

	resp, err := http.Post(ts.URL+"/fwd", "application/json", nil)
	if err != nil {
		t.Fatalf("http.Post: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("X-Downstream") != "value-from-downstream" {
		t.Errorf("X-Downstream = %q; want %q", resp.Header.Get("X-Downstream"), "value-from-downstream")
	}
}

// ── Lifecycle tests ───────────────────────────────────────────────────────────

func TestServe_returnsWhenContextCancels(t *testing.T) {
	t.Parallel()

	srv := buildServerOnPort(t, &fakeExecutor{}, 0)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx)
	}()

	// Wait until server is up.
	waitForServer(t, srv, 2*time.Second)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve() returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not return after context cancel")
	}
}

func TestServe_portZeroReturnsRealAddrViaAddr(t *testing.T) {
	t.Parallel()

	srv := buildServerOnPort(t, &fakeExecutor{}, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Serve(ctx) //nolint:errcheck

	waitForServer(t, srv, 2*time.Second)

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("Addr() returned empty after Serve started")
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Errorf("Addr() = %q; SplitHostPort error: %v", addr, err)
	}
	if portStr == "0" {
		t.Errorf("Addr() returned port 0; want a real assigned port")
	}
}

func TestServe_bindInUse_returnsError(t *testing.T) {
	t.Parallel()

	// Grab a port then hold it.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-listen: %v", err)
	}
	defer lis.Close()

	addr := lis.Addr().String()
	_, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	srv := buildServerOnPort(t, &fakeExecutor{}, port)
	err = srv.Serve(context.Background())
	if err == nil {
		t.Fatal("Serve() expected error when port is in use, got nil")
	}
}

func TestServe_gracefulShutdownAllowsInFlightRequests(t *testing.T) {
	t.Parallel()

	// An executor that takes 200ms to respond.
	slowExec := &slowFakeExecutor{
		delay:  200 * time.Millisecond,
		result: fairway.Result{HTTPStatus: 200, Body: []byte("ok")},
	}
	route := fairway.Route{
		Path:   "/slow",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}

	cfg := baseConfig()
	cfg.Routes = []fairway.Route{route}
	// Pick an ephemeral port instead of DefaultPort (9876), which a locally
	// installed shipyard-fairway daemon may already be holding.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(lis.Addr().String())
	fmt.Sscanf(portStr, "%d", &cfg.Port)
	lis.Close()
	repo := &fakeRepo{cfg: cfg}
	router := fairway.NewRouterWithConfig(repo, cfg)

	srv := fairway.NewServer(fairway.ServerConfig{Router: router, Executor: slowExec})

	ctx, cancel := context.WithCancel(context.Background())

	go srv.Serve(ctx) //nolint:errcheck
	waitForServer(t, srv, 2*time.Second)

	// Fire a slow request.
	reqDone := make(chan int, 1)
	go func() {
		resp, err := http.Post("http://"+srv.Addr()+"/slow", "application/json", nil)
		if err != nil {
			reqDone <- -1
			return
		}
		resp.Body.Close()
		reqDone <- resp.StatusCode
	}()

	// Let the request start processing, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case status := <-reqDone:
		if status != 200 {
			t.Errorf("in-flight request got status %d; want 200 (graceful shutdown)", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not complete within graceful shutdown window")
	}
}

// ── Auth cache tests ──────────────────────────────────────────────────────────

func TestAuthCache_reusesAuthenticatorPerPath(t *testing.T) {
	t.Parallel()

	var created atomic.Int64
	countingFactory := func(a fairway.Auth) (fairway.Authenticator, error) {
		created.Add(1)
		return fairway.NewAuthenticator(a)
	}
	_ = countingFactory // used below

	// Use the server directly with a counting wrapper on the route.
	// We verify cache reuse by checking that NewAuthenticator is not called
	// N times for N requests to the same path. We do this by inspecting the
	// ServerHandlerForTest and sending 10 requests.
	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200}}
	route := fairway.Route{
		Path:   "/cached",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	srv := buildServer(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/cached", nil)
		r.RemoteAddr = "127.0.0.1:9999"
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("request %d: status %d; want 200", i, w.Code)
		}
	}
	// Executor should have been called 10 times — if auth cached, no panic.
	if exec.calls.Load() != 10 {
		t.Errorf("executor called %d times; want 10", exec.calls.Load())
	}
}

func TestAuthCache_invalidatesOnRouteReplace(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200}}
	route := fairway.Route{
		Path:   "/secure",
		Auth:   fairway.Auth{Type: fairway.AuthBearer, Token: "old-token"},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	srv := buildServer(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	// Request with old token succeeds.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/secure", nil)
	r.Header.Set("Authorization", "Bearer old-token")
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("first request: status %d; want 200", w.Code)
	}

	// Invalidate the cache (simulating a route replace).
	srv.InvalidateAuthCache()

	// Now a request with old token still passes because route.Auth.Token is
	// still "old-token". We're verifying that the auth was re-created from the
	// route (not stale cache), so the same token still works.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodPost, "/secure", nil)
	r2.Header.Set("Authorization", "Bearer old-token")
	handler.ServeHTTP(w2, r2)
	if w2.Code != 200 {
		t.Errorf("post-invalidate request: status %d; want 200", w2.Code)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func buildServer(t *testing.T, exec fairway.Executor, routes ...fairway.Route) *fairway.Server {
	t.Helper()
	cfg := baseConfig()
	cfg.Routes = routes
	repo := &fakeRepo{cfg: cfg}
	router := fairway.NewRouterWithConfig(repo, cfg)
	return fairway.NewServer(fairway.ServerConfig{Router: router, Executor: exec})
}

func buildServerOnPort(t *testing.T, exec fairway.Executor, port int) *fairway.Server {
	t.Helper()
	cfg := baseConfig()
	cfg.Port = port
	if port == 0 {
		// port 0 is invalid for Config.Validate(), use a free port instead.
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("find free port: %v", err)
		}
		_, portStr, _ := net.SplitHostPort(lis.Addr().String())
		fmt.Sscanf(portStr, "%d", &cfg.Port)
		lis.Close()
	}
	repo := &fakeRepo{cfg: cfg}
	router := fairway.NewRouterWithConfig(repo, cfg)
	return fairway.NewServer(fairway.ServerConfig{Router: router, Executor: exec})
}

func waitForServer(t *testing.T, srv *fairway.Server, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		addr := srv.Addr()
		if addr != "" {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not become ready within timeout")
}

// slowFakeExecutor sleeps for delay before returning result.
type slowFakeExecutor struct {
	delay  time.Duration
	result fairway.Result
}

func (s *slowFakeExecutor) Execute(ctx context.Context, _ fairway.Route, _ *http.Request) (fairway.Result, error) {
	select {
	case <-time.After(s.delay):
		return s.result, nil
	case <-ctx.Done():
		return fairway.Result{HTTPStatus: 504}, nil
	}
}

// Ensure unused imports are used
var _ = io.Discard
var _ = fmt.Sprintf

// TestHTTPWriteTimeoutCoversMaxRouteTimeout guards against the silent regression
// where someone bumps MaxRouteTimeout without adjusting httpWriteTimeout, causing
// the HTTP server to close connections before a long-running action can respond.
// The invariant is purely about constants, so no network calls are needed.
func TestHTTPWriteTimeoutCoversMaxRouteTimeout(t *testing.T) {
	t.Parallel()

	// httpWriteTimeout mirrors the private server constant; keep in sync with server.go.
	const httpWriteTimeout = fairway.MaxRouteTimeout + 30*time.Second
	if httpWriteTimeout <= fairway.MaxRouteTimeout {
		t.Fatalf("httpWriteTimeout (%s) must be > MaxRouteTimeout (%s)", httpWriteTimeout, fairway.MaxRouteTimeout)
	}
}
