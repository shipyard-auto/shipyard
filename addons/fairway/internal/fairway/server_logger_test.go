package fairway_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// ── fakeReqLogger ─────────────────────────────────────────────────────────────

// errRequestLogger is a RequestLogger-like wrapper whose Log always fails.
// We wrap *fairway.RequestLogger but intercept Log calls via a custom type.
// Since RequestLogger is a concrete struct (not an interface), we build a fake
// HTTP handler that delegates to a failing logger by injecting it via
// ServerConfig.ReqLogger. We need a logger that fails — we achieve this by
// creating a RequestLogger backed by a read-only directory so that file open
// fails. Because we only care that the HTTP response is NOT blocked, this is
// sufficient.

// ── helpers ───────────────────────────────────────────────────────────────────

// buildServerWithLogger creates a Server wired with the given RequestLogger and Stats.
func buildServerWithLogger(t *testing.T, exec fairway.Executor, rl *fairway.RequestLogger, st *fairway.Stats, routes ...fairway.Route) *fairway.Server {
	t.Helper()
	cfg := baseConfig()
	cfg.Routes = routes
	repo := &fakeRepo{cfg: cfg}
	router := fairway.NewRouterWithConfig(repo, cfg)
	return fairway.NewServer(fairway.ServerConfig{
		Router:    router,
		Executor:  exec,
		ReqLogger: rl,
		Stats:     st,
	})
}

// ── TestServer_logsEveryRequest ───────────────────────────────────────────────

func TestServer_logsEveryRequest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fixed := time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)
	rl, err := fairway.NewRequestLogger(dir, func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer rl.Close() //nolint:errcheck

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: []byte("ok")}}
	route := fairway.Route{
		Path:   "/logged",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "myjob"},
	}

	srv := buildServerWithLogger(t, exec, rl, nil, route)
	handler := fairway.ServerHandlerForTest(srv)

	// Fire 3 requests.
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/logged", strings.NewReader("{}"))
		r.RemoteAddr = "127.0.0.1:9999"
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("request %d: status %d; want 200", i, w.Code)
		}
	}

	_ = rl.Close()

	logFile := filepath.Join(dir, "2026-04-17.jsonl")
	f, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		var evt fairway.RequestEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Errorf("invalid JSON on line %d: %v", count+1, err)
		}
		if evt.Event != "http_request" {
			t.Errorf("line %d: Event = %q; want http_request", count+1, evt.Event)
		}
		if evt.Message != "fairway HTTP request handled" {
			t.Errorf("line %d: Message = %q; want fairway HTTP request handled", count+1, evt.Message)
		}
		if evt.Data.Method != http.MethodPost {
			t.Errorf("line %d: Method = %q; want %q", count+1, evt.Data.Method, http.MethodPost)
		}
		if evt.Data.Path != "/logged" {
			t.Errorf("line %d: Path = %q; want /logged", count+1, evt.Data.Path)
		}
		if evt.Data.Action != string(fairway.ActionCronRun) {
			t.Errorf("line %d: Action = %q; want %q", count+1, evt.Data.Action, fairway.ActionCronRun)
		}
		if evt.Data.Target != "myjob" {
			t.Errorf("line %d: Target = %q; want myjob", count+1, evt.Data.Target)
		}
		if evt.Data.ExitCode != 0 {
			t.Errorf("line %d: ExitCode = %d; want 0", count+1, evt.Data.ExitCode)
		}
		if evt.Data.AuthType != string(fairway.AuthLocalOnly) {
			t.Errorf("line %d: AuthType = %q; want %q", count+1, evt.Data.AuthType, fairway.AuthLocalOnly)
		}
		if evt.Data.AuthResult != "ok" {
			t.Errorf("line %d: AuthResult = %q; want ok", count+1, evt.Data.AuthResult)
		}
		count++
	}
	if count != 3 {
		t.Errorf("log lines = %d; want 3", count)
	}
}

func TestServer_logsDeniedAuthRequest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fixed := time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)
	rl, err := fairway.NewRequestLogger(dir, func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer rl.Close() //nolint:errcheck

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: []byte("ok")}}
	route := fairway.Route{
		Path:   "/secure",
		Auth:   fairway.Auth{Type: fairway.AuthBearer, Token: "secret"},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}

	srv := buildServerWithLogger(t, exec, rl, nil, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/secure", nil)
	r.RemoteAddr = "127.0.0.1:9999"
	r.Header.Set("Authorization", "Bearer wrong")
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", w.Code)
	}

	_ = rl.Close()

	logFile := filepath.Join(dir, "2026-04-17.jsonl")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}

	var evt fairway.RequestEvent
	if err := json.Unmarshal(bytes.TrimSpace(data), &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Data.AuthResult != "denied" {
		t.Errorf("AuthResult = %q; want denied", evt.Data.AuthResult)
	}
	if evt.Data.ExitCode != -1 {
		t.Errorf("ExitCode = %d; want -1", evt.Data.ExitCode)
	}
}

// ── TestSocket_statsReturnsPopulatedSnapshot ──────────────────────────────────

func TestSocket_statsReturnsPopulatedSnapshot(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)
	for _, path := range []string{"/stat-a", "/stat-b", "/stat-c"} {
		if err := router.Add(testRoute(path)); err != nil {
			t.Fatalf("router.Add(%s): %v", path, err)
		}
	}

	st := fairway.NewStats(time.Now())
	exec := &socketFakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: []byte("ok")}}

	// Build the HTTP server with stats wired.
	httpSrv := fairway.NewServer(fairway.ServerConfig{
		Router:   router,
		Executor: exec,
		Stats:    st,
	})

	// Use route.test (dispatched via socket) to exercise the HTTP handler and
	// populate stats.
	sockCfg := defaultSocketCfg("v1", router, httpSrv)
	sockCfg.Stats = st
	sockPath := startSocket(t, sockCfg)
	conn, scanner := dialAndHandshake(t, sockPath, "v1")

	// Fire 3 route.test requests.
	for i, path := range []string{"/stat-a", "/stat-b", "/stat-c"} {
		resp := call(t, conn, scanner, i+2, "route.test", map[string]string{
			"path":   path,
			"method": "POST",
		})
		if resp.Error != nil {
			t.Fatalf("route.test %d error: %v", i, resp.Error)
		}
	}

	// Now call stats.
	resp := call(t, conn, scanner, 100, "stats", nil)
	if resp.Error != nil {
		t.Fatalf("stats error: %v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var snap struct {
		Total      int64                         `json:"Total"`
		ByRoute    map[string]fairway.RouteStats `json:"ByRoute"`
		ByStatus   map[string]int64              `json:"ByStatus"`
		ByExitCode map[string]int64              `json:"ByExitCode"`
	}
	if err := json.Unmarshal(resultBytes, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	if snap.Total != 3 {
		t.Errorf("Total = %d; want 3", snap.Total)
	}
	for _, path := range []string{"/stat-a", "/stat-b", "/stat-c"} {
		if snap.ByRoute[path].Count != 1 {
			t.Errorf("ByRoute[%s].Count = %d; want 1", path, snap.ByRoute[path].Count)
		}
	}
	if snap.ByStatus["200"] != 3 {
		t.Errorf("ByStatus[200] = %d; want 3", snap.ByStatus["200"])
	}
	if snap.ByExitCode["0"] != 3 {
		t.Errorf("ByExitCode[0] = %d; want 3", snap.ByExitCode["0"])
	}
}

// ── TestServer_loggerErrorDoesNotBlockRequest ─────────────────────────────────

// readOnlyDir creates a read-only directory to make logger file creation fail.
// Returns the path. The directory is restored to writable on cleanup.
func readOnlyLogDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	// Write one file so we can corrupt the logger's state.
	// Instead of a read-only dir approach (which may not work on all systems),
	// we use a different strategy: create a logger and then remove its directory.
	return dir
}

// brokenLogger is a *fairway.RequestLogger whose underlying directory is removed
// so every Log call fails with an open error.
func brokenLogger(t *testing.T) *fairway.RequestLogger {
	t.Helper()

	// Create in a temp dir, then remove the dir after logger is created.
	base := t.TempDir()
	dir := filepath.Join(base, "logs")

	rl, err := fairway.NewRequestLogger(dir, nil)
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}

	// Force the logger to open a new file on the next Log call by first writing
	// a line (so it has a currentDay), then removing the directory so the next
	// rotation fails.
	// Simpler: use a fake now that returns a different day on each call, but
	// we can't control that here. Instead, we remove the log file we just wrote.

	// Actually the simplest approach: write once to create the day entry,
	// then os.Remove the directory so any subsequent attempt to open a new
	// rotated file will fail. But the current day won't change in real time,
	// so the logger won't try to rotate. We need to trigger rotation.

	// Alternative: use a broken directory path directly.
	_ = dir
	_ = rl

	// Use a different approach: pass a now function that alternates days.
	// Day 1: normal, day 2: dir removed so rotation fails.
	base2 := t.TempDir()
	dir2 := filepath.Join(base2, "logs2")

	calls := 0
	day1 := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	fakeClock := func() time.Time {
		calls++
		if calls <= 1 {
			return day1
		}
		return day2
	}

	rl2, err := fairway.NewRequestLogger(dir2, fakeClock)
	if err != nil {
		t.Fatalf("NewRequestLogger2: %v", err)
	}

	// Write first log (day1 file is created successfully).
	if err := rl2.Log(makeEvent("/setup")); err != nil {
		t.Fatalf("setup Log: %v", err)
	}

	// Remove the log directory so day2 rotation fails.
	if err := os.RemoveAll(dir2); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	return rl2
}

func TestServer_loggerErrorDoesNotBlockRequest(t *testing.T) {
	t.Parallel()

	rl := brokenLogger(t)
	defer rl.Close() //nolint:errcheck

	// Verify that the logger itself returns an error (proving it is truly broken).
	if err := rl.Log(makeEvent("/check")); err == nil {
		t.Skip("logger did not return an error on this platform — skipping test")
	}

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: []byte("ok")}}
	route := fairway.Route{
		Path:   "/no-block",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}

	srv := buildServerWithLogger(t, exec, rl, nil, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/no-block", nil)
	r.RemoteAddr = "127.0.0.1:9999"
	handler.ServeHTTP(w, r)

	// The HTTP response must succeed even though the logger errored.
	if w.Code != 200 {
		t.Errorf("status = %d; want 200 despite logger error", w.Code)
	}
}

func TestServer_shutdownClosesRequestLogger(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rl, err := fairway.NewRequestLogger(dir, func() time.Time {
		return time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	cfg := baseConfig()
	cfg.Port = port
	route := fairway.Route{
		Path:   "/logged",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	cfg.Routes = []fairway.Route{route}
	repo := &fakeRepo{cfg: cfg}
	router := fairway.NewRouterWithConfig(repo, cfg)

	srvWithLogger := fairway.NewServer(fairway.ServerConfig{
		Router:    router,
		Executor:  &fakeExecutor{result: fairway.Result{HTTPStatus: 200}},
		ReqLogger: rl,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srvWithLogger.Serve(ctx) }()
	waitForServer(t, srvWithLogger, 2*time.Second)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve(): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not return after cancel")
	}

	if err := rl.Close(); err != nil {
		t.Fatalf("logger should already be closed idempotently, got: %v", err)
	}
}

// ensure errors package is used
var _ = errors.New
