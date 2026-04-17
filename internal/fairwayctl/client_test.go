package fairwayctl_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

// startFakeDaemon creates a Unix socket server at a temp path and returns
// configured Opts. The handler is called for each NDJSON line received per
// connection; returning nil closes that connection.
func startFakeDaemon(t *testing.T, handler func(line []byte) []byte) fairwayctl.Opts {
	t.Helper()

	// Use os.MkdirTemp with short prefix to stay under macOS 104-byte socket
	// path limit.
	dir, err := os.MkdirTemp("", "fw")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	sockPath := filepath.Join(dir, "fairway.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveTestConn(conn, handler)
		}
	}()

	return fairwayctl.Opts{SocketPath: sockPath, Version: "test"}
}

func serveTestConn(conn net.Conn, handler func([]byte) []byte) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		resp := handler(sc.Bytes())
		if resp == nil {
			return
		}
		conn.Write(append(resp, '\n'))
	}
}

// withHandshake wraps a handler so that the first call (handshake) is answered
// automatically with success for the given version, and subsequent calls are
// delegated to next.
func withHandshake(version string, next func([]byte) []byte) func([]byte) []byte {
	var once sync.Once
	done := false
	return func(line []byte) []byte {
		var first bool
		once.Do(func() { first = true })
		if first {
			done = true
			var req struct {
				ID json.RawMessage `json:"id"`
			}
			json.Unmarshal(line, &req)
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]string{"daemonVersion": version},
			})
			return resp
		}
		_ = done
		return next(line)
	}
}

// okResponse builds a success JSON-RPC response for the given request ID.
func okResponse(id json.RawMessage, result any) []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	return b
}

// errResponse builds an error JSON-RPC response.
func errResponse(id json.RawMessage, code int, msg string, data any) []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": msg,
			"data":    data,
		},
	})
	return b
}

// parseID extracts the JSON-RPC "id" field from a raw request line.
func parseID(line []byte) json.RawMessage {
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	json.Unmarshal(line, &req)
	return req.ID
}

// dialTest is a helper that connects to the fake daemon and fatals on error.
func dialTest(t *testing.T, opts fairwayctl.Opts) *fairwayctl.Client {
	t.Helper()
	c, err := fairwayctl.Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// ── Dial tests ────────────────────────────────────────────────────────────────

func TestDial_socketAbsent_returnsErrDaemonNotRunning(t *testing.T) {
	_, err := fairwayctl.Dial(context.Background(), fairwayctl.Opts{
		SocketPath: filepath.Join(t.TempDir(), "no-such.sock"),
		Version:    "test",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, fairwayctl.ErrDaemonNotRunning) {
		t.Errorf("want ErrDaemonNotRunning, got %v", err)
	}
}

func TestDial_handshakeOK_returnsClient(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		return nil // no further messages expected
	}))
	c, err := fairwayctl.Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	c.Close()
}

func TestDial_versionMismatch_returnsErrVersionMismatch(t *testing.T) {
	opts := startFakeDaemon(t, func(line []byte) []byte {
		id := parseID(line)
		return errResponse(id, -32010, "version mismatch", map[string]string{
			"daemon": "0.21",
			"client": "0.20",
		})
	})
	opts.Version = "0.20"

	_, err := fairwayctl.Dial(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var vmErr *fairwayctl.ErrVersionMismatch
	if !errors.As(err, &vmErr) {
		t.Fatalf("want *ErrVersionMismatch, got %T: %v", err, err)
	}
	if vmErr.Daemon != "0.21" {
		t.Errorf("want Daemon=0.21, got %q", vmErr.Daemon)
	}
	if vmErr.Client != "0.20" {
		t.Errorf("want Client=0.20, got %q", vmErr.Client)
	}
}

func TestDial_malformedResponse_returnsError(t *testing.T) {
	opts := startFakeDaemon(t, func(line []byte) []byte {
		return []byte(`not valid json {{{`)
	})

	_, err := fairwayctl.Dial(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDial_respectsContextDeadline(t *testing.T) {
	// Daemon accepts connections but never sends the handshake response.
	dir, err := os.MkdirTemp("", "fw")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	sockPath := filepath.Join(dir, "fairway.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Accept but never respond.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open without writing anything.
			t.Cleanup(func() { conn.Close() })
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = fairwayctl.Dial(ctx, fairwayctl.Opts{SocketPath: sockPath, Version: "test"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Dial took %v, want < 500ms", elapsed)
	}
}

// ── RPC method tests ──────────────────────────────────────────────────────────

func TestRouteList_returnsRoutes(t *testing.T) {
	want := []fairwayctl.Route{
		{
			Path:   "/hooks/test",
			Auth:   fairwayctl.Auth{Type: fairwayctl.AuthBearer, Token: "secret"},
			Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "ABC123"},
		},
	}

	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return okResponse(id, want)
	}))
	c := dialTest(t, opts)

	routes, err := c.RouteList(context.Background())
	if err != nil {
		t.Fatalf("RouteList: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("want 1 route, got %d", len(routes))
	}
	if routes[0].Path != "/hooks/test" {
		t.Errorf("want path /hooks/test, got %q", routes[0].Path)
	}
}

func TestRouteAdd_success(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return okResponse(id, map[string]bool{"ok": true})
	}))
	c := dialTest(t, opts)

	err := c.RouteAdd(context.Background(), fairwayctl.Route{
		Path:   "/hooks/test",
		Auth:   fairwayctl.Auth{Type: fairwayctl.AuthLocalOnly},
		Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "ABC123"},
	})
	if err != nil {
		t.Errorf("RouteAdd: unexpected error: %v", err)
	}
}

func TestRouteAdd_duplicate_returnsErrDuplicatePath(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return errResponse(id, -32602, `duplicate route path: "/hooks/test"`, nil)
	}))
	c := dialTest(t, opts)

	err := c.RouteAdd(context.Background(), fairwayctl.Route{
		Path:   "/hooks/test",
		Auth:   fairwayctl.Auth{Type: fairwayctl.AuthLocalOnly},
		Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "ABC123"},
	})
	if !errors.Is(err, fairwayctl.ErrDuplicatePath) {
		t.Errorf("want ErrDuplicatePath, got %v", err)
	}
}

func TestRouteDelete_success(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return okResponse(id, map[string]bool{"ok": true})
	}))
	c := dialTest(t, opts)

	err := c.RouteDelete(context.Background(), "/hooks/test")
	if err != nil {
		t.Errorf("RouteDelete: unexpected error: %v", err)
	}
}

func TestRouteDelete_notFound_returnsErrRouteNotFound(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return errResponse(id, -32602, `route not found: "/hooks/missing"`, nil)
	}))
	c := dialTest(t, opts)

	err := c.RouteDelete(context.Background(), "/hooks/missing")
	if !errors.Is(err, fairwayctl.ErrRouteNotFound) {
		t.Errorf("want ErrRouteNotFound, got %v", err)
	}
}

func TestStatus_returnsInfo(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return okResponse(id, map[string]any{
			"version":    "test",
			"startedAt":  "2024-01-01T00:00:00Z",
			"uptime":     "1h0m0s",
			"port":       9876,
			"bind":       "127.0.0.1",
			"routeCount": 2,
			"inFlight":   0,
		})
	}))
	c := dialTest(t, opts)

	info, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if info.Version != "test" {
		t.Errorf("want version=test, got %q", info.Version)
	}
	if info.Port != 9876 {
		t.Errorf("want port=9876, got %d", info.Port)
	}
	if info.RouteCount != 2 {
		t.Errorf("want routeCount=2, got %d", info.RouteCount)
	}
}

func TestStats_returnsSnapshot(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return okResponse(id, map[string]any{
			"Total":      int64(10),
			"ByRoute":    map[string]any{},
			"ByStatus":   map[string]any{"200": int64(10)},
			"ByExitCode": map[string]any{"0": int64(10)},
			"StartedAt":  time.Time{},
		})
	}))
	c := dialTest(t, opts)

	snap, err := c.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if snap.Total != 10 {
		t.Errorf("want Total=10, got %d", snap.Total)
	}
}

func TestRouteTest_returnsTestResult(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return okResponse(id, map[string]any{
			"status":     200,
			"body":       "OK",
			"durationMs": int64(5),
		})
	}))
	c := dialTest(t, opts)

	result, err := c.RouteTest(context.Background(), "/hooks/test", "POST", []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("RouteTest: %v", err)
	}
	if result.Status != 200 {
		t.Errorf("want status=200, got %d", result.Status)
	}
	if result.Body != "OK" {
		t.Errorf("want body=OK, got %q", result.Body)
	}
}

// ── Error propagation tests ───────────────────────────────────────────────────

func TestUnknownMethod_returnsGenericRPCError(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return errResponse(id, -32601, `method "route.list" not found`, nil)
	}))
	c := dialTest(t, opts)

	_, err := c.RouteList(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rpcErr *fairwayctl.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("want *RPCError in chain, got %T: %v", err, err)
	}
	if rpcErr.Code != -32601 {
		t.Errorf("want code=-32601, got %d", rpcErr.Code)
	}
}

func TestConnectionDropped_nextCallReturnsError(t *testing.T) {
	var connMu sync.Mutex
	var serverConn net.Conn

	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		// On the first real request, drop the connection silently.
		connMu.Lock()
		c := serverConn
		connMu.Unlock()
		if c != nil {
			c.Close()
		}
		return nil
	}))

	// Capture the server-side connection so we can close it.
	dir, _ := os.MkdirTemp("", "fw2")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath2 := filepath.Join(dir, "fairway.sock")
	ln2, err := net.Listen("unix", sockPath2)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln2.Close() })

	go func() {
		conn, err := ln2.Accept()
		if err != nil {
			return
		}
		connMu.Lock()
		serverConn = conn
		connMu.Unlock()
		serveTestConn(conn, withHandshake("test", func(line []byte) []byte {
			// Close the connection on the first real request.
			conn.Close()
			return nil
		}))
	}()

	c, err := fairwayctl.Dial(context.Background(), fairwayctl.Opts{
		SocketPath: sockPath2,
		Version:    "test",
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	_, err = c.RouteList(context.Background())
	if err == nil {
		t.Fatal("expected error after connection drop, got nil")
	}

	_ = opts // silence unused warning — opts is from the earlier fake daemon setup
}

// ── Additional coverage tests ─────────────────────────────────────────────────

func TestErrVersionMismatch_errorString(t *testing.T) {
	e := &fairwayctl.ErrVersionMismatch{Daemon: "0.21", Client: "0.20"}
	got := e.Error()
	if got == "" {
		t.Error("Error() returned empty string")
	}
	for _, want := range []string{"0.21", "0.20"} {
		if !containsStr(got, want) {
			t.Errorf("Error() %q does not contain %q", got, want)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

func TestCall_idMismatch_returnsError(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		// Return a response with a wrong ID.
		return okResponse(json.RawMessage(`9999`), []fairwayctl.Route{})
	}))
	c := dialTest(t, opts)

	_, err := c.RouteList(context.Background())
	if err == nil {
		t.Fatal("expected id mismatch error, got nil")
	}
}

func TestCall_malformedResponseJSON_returnsError(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		return []byte(`{invalid json`)
	}))
	c := dialTest(t, opts)

	_, err := c.RouteList(context.Background())
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestRouteList_malformedResult_returnsError(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		// result is a number, not a JSON array of Routes.
		return okResponse(id, 42)
	}))
	c := dialTest(t, opts)

	_, err := c.RouteList(context.Background())
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestStatus_malformedResult_returnsError(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		// "port" as a string breaks unmarshal into StatusInfo.Port (int).
		return okResponse(id, map[string]any{"port": "not-a-number"})
	}))
	c := dialTest(t, opts)

	_, err := c.Status(context.Background())
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestStats_malformedResult_returnsError(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return okResponse(id, "not-a-snapshot")
	}))
	c := dialTest(t, opts)

	_, err := c.Stats(context.Background())
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestRouteTest_malformedResult_returnsError(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return okResponse(id, "not-a-test-result")
	}))
	c := dialTest(t, opts)

	_, err := c.RouteTest(context.Background(), "/hooks/test", "POST", nil, nil)
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestCall_withContextDeadline_setsDeadline(t *testing.T) {
	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		return okResponse(id, []fairwayctl.Route{})
	}))
	c := dialTest(t, opts)

	// A ctx with a deadline that's in the future should succeed normally.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.RouteList(ctx)
	if err != nil {
		t.Errorf("RouteList with deadline context: %v", err)
	}
}

// ── Thread-safety test ────────────────────────────────────────────────────────

func TestClient_concurrentCallsAreSafe(t *testing.T) {
	var mu sync.Mutex
	counter := 0

	opts := startFakeDaemon(t, withHandshake("test", func(line []byte) []byte {
		id := parseID(line)
		mu.Lock()
		counter++
		mu.Unlock()
		return okResponse(id, []fairwayctl.Route{})
	}))
	c := dialTest(t, opts)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.RouteList(context.Background())
		}()
	}
	wg.Wait()

	// All 5 calls should have reached the handler.
	mu.Lock()
	got := counter
	mu.Unlock()
	if got != 5 {
		t.Errorf("want 5 calls, got %d", got)
	}
}
