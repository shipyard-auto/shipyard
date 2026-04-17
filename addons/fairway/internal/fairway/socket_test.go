package fairway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	fairway "github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

// newTestRouter creates an in-memory router backed by a temp-dir file.
func newTestRouter(t *testing.T) *fairway.Router {
	t.Helper()
	repo := fairway.NewFileRepositoryAt(filepath.Join(t.TempDir(), "routes.json"))
	cfg := fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          fairway.DefaultPort,
		Bind:          fairway.DefaultBind,
		MaxInFlight:   fairway.DefaultMaxInFlight,
		Routes:        []fairway.Route{},
	}
	return fairway.NewRouterWithConfig(repo, cfg)
}

// newTestHTTPServer creates a *Server backed by a fake executor.
// The server is NOT started (no Serve call); only the handler is used.
func newTestHTTPServer(t *testing.T, router *fairway.Router, result fairway.Result) *fairway.Server {
	t.Helper()
	exec := &socketFakeExecutor{result: result}
	return fairway.NewServer(fairway.ServerConfig{
		Router:   router,
		Executor: exec,
	})
}

// socketFakeExecutor implements fairway.Executor for socket tests.
type socketFakeExecutor struct {
	result fairway.Result
	err    error
}

func (e *socketFakeExecutor) Execute(_ context.Context, _ fairway.Route, _ *http.Request) (fairway.Result, error) {
	return e.result, e.err
}

// shortSockPath creates a short socket path in /tmp to stay under the 104-byte
// macOS Unix socket path limit.
func shortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fw")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// startSocket starts a SocketServer in the background and returns the socket
// path plus a cleanup function that cancels it and waits for shutdown.
func startSocket(t *testing.T, cfg fairway.SocketConfig) string {
	t.Helper()
	sockPath := shortSockPath(t)
	cfg.Path = sockPath
	ss := fairway.NewSocketServer(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ss.Serve(ctx) }()

	// Wait for socket file to appear (up to 5s).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("socket server exited before becoming ready: %v", err)
		default:
		}
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("socket server shutdown: %v", err)
		}
	})
	return sockPath
}

// dialAndHandshake opens a connection, performs a successful handshake, and
// returns the connection and a reusable scanner. Fails the test on any error.
// Retries the dial for up to 5 seconds to handle the window between the socket
// file appearing (after net.Listen) and the accept loop being ready.
func dialAndHandshake(t *testing.T, sockPath, version string) (net.Conn, *bufio.Scanner) {
	t.Helper()
	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", sockPath)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial %s: could not connect within 5s", sockPath)
	}
	t.Cleanup(func() { conn.Close() })

	scanner := bufio.NewScanner(conn)

	// Send handshake.
	sendRPC(t, conn, 0, "handshake", map[string]string{"version": version})

	// Read handshake response.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !scanner.Scan() {
		t.Fatalf("read handshake response: %v", scanner.Scan())
	}
	_ = conn.SetReadDeadline(time.Time{})

	var resp fairway.Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("parse handshake response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("handshake returned error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	return conn, scanner
}

// sendRPC encodes a JSON-RPC request and writes it to conn.
func sendRPC(t *testing.T, conn net.Conn, id int, method string, params any) {
	t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		raw = b
	}
	req := fairway.Request{
		JSONRPC: fairway.JSONRPCVersion,
		ID:      json.RawMessage(fmt.Sprintf("%d", id)),
		Method:  method,
		Params:  raw,
	}
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write rpc: %v", err)
	}
}

// readRPC reads the next NDJSON line and unmarshals it as a Response.
func readRPC(t *testing.T, conn net.Conn, scanner *bufio.Scanner) fairway.Response {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !scanner.Scan() {
		t.Fatalf("scan response: %v", scanner.Err())
	}
	_ = conn.SetReadDeadline(time.Time{})

	var resp fairway.Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	return resp
}

// call is a convenience wrapper: sendRPC + readRPC.
func call(t *testing.T, conn net.Conn, scanner *bufio.Scanner, id int, method string, params any) fairway.Response {
	t.Helper()
	sendRPC(t, conn, id, method, params)
	return readRPC(t, conn, scanner)
}

// defaultSocketCfg builds a minimal SocketConfig for tests.
func defaultSocketCfg(version string, router *fairway.Router, srv *fairway.Server) fairway.SocketConfig {
	return fairway.SocketConfig{
		Router:  router,
		Server:  srv,
		Version: version,
		Now:     time.Now,
	}
}

// testRoute returns a valid Route for use in tests.
func testRoute(path string) fairway.Route {
	return fairway.Route{
		Path: path,
		Auth: fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{
			Type:   fairway.ActionCronRun,
			Target: "myjob",
		},
	}
}

// ── Handshake tests ───────────────────────────────────────────────────────────

func TestHandshake_success_returnsDaemonVersion(t *testing.T) {
	t.Parallel()

	sockPath := startSocket(t, defaultSocketCfg("v1.2.3", newTestRouter(t), nil))
	conn, scanner := dialAndHandshake(t, sockPath, "v1.2.3")
	_ = conn
	_ = scanner
	// dialAndHandshake already asserted no error and the response was valid.
}

func TestHandshake_versionMismatch_returnsError_closesConn(t *testing.T) {
	t.Parallel()

	sockPath := startSocket(t, defaultSocketCfg("daemon-v2", newTestRouter(t), nil))

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	sendRPC(t, conn, 1, "handshake", map[string]string{"version": "client-v1"})

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !scanner.Scan() {
		t.Fatalf("scan: %v", scanner.Err())
	}
	_ = conn.SetReadDeadline(time.Time{})

	var resp fairway.Response
	json.Unmarshal(scanner.Bytes(), &resp)

	if resp.Error == nil || resp.Error.Code != fairway.ErrCodeVersionMismatch {
		t.Errorf("error.code = %v; want %d", resp.Error, fairway.ErrCodeVersionMismatch)
	}

	// Verify data fields.
	if resp.Error.Data != nil {
		dataBytes, _ := json.Marshal(resp.Error.Data)
		var d map[string]string
		json.Unmarshal(dataBytes, &d)
		if d["daemon"] != "daemon-v2" {
			t.Errorf("data.daemon = %q; want %q", d["daemon"], "daemon-v2")
		}
		if d["client"] != "client-v1" {
			t.Errorf("data.client = %q; want %q", d["client"], "client-v1")
		}
	}

	// Connection should be closed by server.
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if scanner.Scan() {
		t.Error("expected conn to be closed after version mismatch")
	} else if err := scanner.Err(); err != nil {
		t.Errorf("expected clean EOF; got: %v", err)
	}
}

func TestHandshake_timeout_closesConn(t *testing.T) {
	t.Parallel()

	cfg := defaultSocketCfg("v1", newTestRouter(t), nil)
	cfg.HandshakeTimeout = 200 * time.Millisecond
	sockPath := startSocket(t, cfg)

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Don't send anything. Server should time out and send ErrCodeHandshakeTimeout.
	scanner := bufio.NewScanner(conn)
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	if !scanner.Scan() {
		t.Fatalf("expected error response before conn close: %v", scanner.Err())
	}
	_ = conn.SetReadDeadline(time.Time{})

	var resp fairway.Response
	json.Unmarshal(scanner.Bytes(), &resp)

	if resp.Error == nil || resp.Error.Code != fairway.ErrCodeHandshakeTimeout {
		t.Errorf("error.code = %v; want %d", resp.Error, fairway.ErrCodeHandshakeTimeout)
	}

	// Connection must be closed now.
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if scanner.Scan() {
		t.Error("expected conn to be closed after timeout")
	} else if err := scanner.Err(); err != nil {
		t.Errorf("expected clean EOF; got: %v", err)
	}
}

func TestHandshake_wrongMethodFirst_returnsRequired(t *testing.T) {
	t.Parallel()

	sockPath := startSocket(t, defaultSocketCfg("v1", newTestRouter(t), nil))

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	sendRPC(t, conn, 1, "route.list", nil)

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !scanner.Scan() {
		t.Fatalf("scan: %v", scanner.Err())
	}

	var resp fairway.Response
	json.Unmarshal(scanner.Bytes(), &resp)

	if resp.Error == nil || resp.Error.Code != fairway.ErrCodeHandshakeRequired {
		t.Errorf("error.code = %v; want %d", resp.Error, fairway.ErrCodeHandshakeRequired)
	}
}

func TestHandshake_malformedJSON_returnsParseError_closesConn(t *testing.T) {
	t.Parallel()

	sockPath := startSocket(t, defaultSocketCfg("v1", newTestRouter(t), nil))

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Write malformed JSON.
	conn.Write([]byte("{not valid json}\n"))

	scanner := bufio.NewScanner(conn)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !scanner.Scan() {
		t.Fatalf("scan: %v", scanner.Err())
	}

	var resp fairway.Response
	json.Unmarshal(scanner.Bytes(), &resp)

	if resp.Error == nil || resp.Error.Code != fairway.ErrCodeParseError {
		t.Errorf("error.code = %v; want %d", resp.Error, fairway.ErrCodeParseError)
	}

	// Connection should be closed.
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if scanner.Scan() {
		t.Error("expected conn to be closed after parse error")
	} else if err := scanner.Err(); err != nil {
		t.Errorf("expected clean EOF; got: %v", err)
	}
}

// ── Dispatch tests ────────────────────────────────────────────────────────────

func TestDispatch_unknownMethod_returnsMethodNotFound(t *testing.T) {
	t.Parallel()

	sockPath := startSocket(t, defaultSocketCfg("v1", newTestRouter(t), nil))
	conn, scanner := dialAndHandshake(t, sockPath, "v1")

	resp := call(t, conn, scanner, 2, "bogus.method", nil)

	if resp.Error == nil || resp.Error.Code != fairway.ErrCodeMethodNotFound {
		t.Errorf("error.code = %v; want %d", resp.Error, fairway.ErrCodeMethodNotFound)
	}
}

func TestRouteList_returnsCurrentRoutes(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)
	_ = router.Add(testRoute("/hook1"))
	_ = router.Add(testRoute("/hook2"))

	sockPath := startSocket(t, defaultSocketCfg("v1", router, nil))
	conn, scanner := dialAndHandshake(t, sockPath, "v1")

	resp := call(t, conn, scanner, 2, "route.list", nil)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var routes []fairway.Route
	json.Unmarshal(resultBytes, &routes)

	if len(routes) != 2 {
		t.Errorf("len(routes) = %d; want 2", len(routes))
	}
}

func TestRouteAdd_persistsViaRouter(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "routes.json")
	repo := fairway.NewFileRepositoryAt(configPath)
	cfg := fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          fairway.DefaultPort,
		Bind:          fairway.DefaultBind,
		MaxInFlight:   fairway.DefaultMaxInFlight,
		Routes:        []fairway.Route{},
	}
	router := fairway.NewRouterWithConfig(repo, cfg)

	sockPath := startSocket(t, defaultSocketCfg("v1", router, nil))
	conn, scanner := dialAndHandshake(t, sockPath, "v1")

	route := testRoute("/new-route")
	resp := call(t, conn, scanner, 2, "route.add", map[string]any{"route": route})

	if resp.Error != nil {
		t.Fatalf("route.add error: %v", resp.Error)
	}

	// Verify in-memory.
	routes := router.List()
	if len(routes) != 1 || routes[0].Path != "/new-route" {
		t.Errorf("expected /new-route in router; got %v", routes)
	}

	// Verify persisted to disk.
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("routes.json not created: %v", err)
	}
}

func TestRouteAdd_invalidRoute_returnsInvalidParams(t *testing.T) {
	t.Parallel()

	sockPath := startSocket(t, defaultSocketCfg("v1", newTestRouter(t), nil))
	conn, scanner := dialAndHandshake(t, sockPath, "v1")

	// Route with invalid path (no leading slash).
	badRoute := fairway.Route{
		Path:   "no-slash",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	resp := call(t, conn, scanner, 2, "route.add", map[string]any{"route": badRoute})

	if resp.Error == nil || resp.Error.Code != fairway.ErrCodeInvalidParams {
		t.Errorf("error.code = %v; want %d", resp.Error, fairway.ErrCodeInvalidParams)
	}
}

func TestRouteAdd_duplicatePath_returnsError(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)
	_ = router.Add(testRoute("/dup"))

	sockPath := startSocket(t, defaultSocketCfg("v1", router, nil))
	conn, scanner := dialAndHandshake(t, sockPath, "v1")

	resp := call(t, conn, scanner, 2, "route.add", map[string]any{"route": testRoute("/dup")})

	if resp.Error == nil {
		t.Error("expected error for duplicate path")
	}
}

func TestRouteDelete_removesExisting(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)
	_ = router.Add(testRoute("/to-delete"))

	sockPath := startSocket(t, defaultSocketCfg("v1", router, nil))
	conn, scanner := dialAndHandshake(t, sockPath, "v1")

	resp := call(t, conn, scanner, 2, "route.delete", map[string]string{"path": "/to-delete"})

	if resp.Error != nil {
		t.Fatalf("route.delete error: %v", resp.Error)
	}
	if len(router.List()) != 0 {
		t.Error("expected empty route list after delete")
	}
}

func TestRouteDelete_missing_returnsError(t *testing.T) {
	t.Parallel()

	sockPath := startSocket(t, defaultSocketCfg("v1", newTestRouter(t), nil))
	conn, scanner := dialAndHandshake(t, sockPath, "v1")

	resp := call(t, conn, scanner, 2, "route.delete", map[string]string{"path": "/does-not-exist"})

	if resp.Error == nil {
		t.Error("expected error for missing route")
	}
}

func TestRouteTest_invokesHandlerInMemory(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)
	route := testRoute("/test-me")
	_ = router.Add(route)

	srv := newTestHTTPServer(t, router, fairway.Result{
		HTTPStatus: 202,
		Body:       []byte("dispatched"),
	})

	sockPath := startSocket(t, defaultSocketCfg("v1", router, srv))
	conn, scanner := dialAndHandshake(t, sockPath, "v1")

	resp := call(t, conn, scanner, 2, "route.test", map[string]string{
		"path":   "/test-me",
		"method": "POST",
	})

	if resp.Error != nil {
		t.Fatalf("route.test error: %v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]any
	json.Unmarshal(resultBytes, &result)

	status := int(result["status"].(float64))
	if status != 202 {
		t.Errorf("status = %d; want 202", status)
	}
}

func TestStatus_returnsExpectedFields(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)
	_ = router.Add(testRoute("/r1"))

	cfg := defaultSocketCfg("v1.0.0", router, nil)
	cfg.Now = func() time.Time {
		return time.Date(2026, 4, 17, 12, 0, 30, 0, time.UTC)
	}
	sockPath := startSocket(t, cfg)
	conn, scanner := dialAndHandshake(t, sockPath, "v1.0.0")

	resp := call(t, conn, scanner, 2, "status", nil)

	if resp.Error != nil {
		t.Fatalf("status error: %v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]any
	json.Unmarshal(resultBytes, &result)

	if result["version"] != "v1.0.0" {
		t.Errorf("version = %q; want %q", result["version"], "v1.0.0")
	}
	if result["port"] == nil {
		t.Error("expected port field")
	}
	if result["bind"] == nil {
		t.Error("expected bind field")
	}
	if result["startedAt"] == nil {
		t.Error("expected startedAt field")
	}
	if result["uptime"] == nil {
		t.Error("expected uptime field")
	}
	if result["inFlight"] == nil {
		t.Error("expected inFlight field")
	}
	if result["inflight"] == nil {
		t.Error("expected inflight compatibility field")
	}
	routeCount := int(result["routeCount"].(float64))
	if routeCount != 1 {
		t.Errorf("routeCount = %d; want 1", routeCount)
	}
	if inFlight := int(result["inFlight"].(float64)); inFlight != 0 {
		t.Errorf("inFlight = %d; want 0", inFlight)
	}
	if inflightCompat := int(result["inflight"].(float64)); inflightCompat != 0 {
		t.Errorf("inflight = %d; want 0", inflightCompat)
	}
}

// ── Concurrency tests ─────────────────────────────────────────────────────────

func TestMultipleClients_independentSessions(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)
	sockPath := startSocket(t, defaultSocketCfg("v2", router, nil))

	const clients = 5
	errs := make(chan error, clients)

	for i := 0; i < clients; i++ {
		go func(id int) {
			conn, scanner := dialAndHandshake(t, sockPath, "v2")
			_ = conn
			resp := call(t, conn, scanner, id, "route.list", nil)
			if resp.Error != nil {
				errs <- fmt.Errorf("client %d: route.list error: %v", id, resp.Error)
				return
			}
			errs <- nil
		}(i)
	}

	for i := 0; i < clients; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestInvalidatesAuthCacheAfterMutation(t *testing.T) {
	t.Parallel()

	var invalidations atomic.Int32
	cfg := defaultSocketCfg("v1", newTestRouter(t), nil)
	cfg.OnInvalidate = func() { invalidations.Add(1) }

	sockPath := startSocket(t, cfg)
	conn, scanner := dialAndHandshake(t, sockPath, "v1")

	// Add route — should trigger invalidation.
	resp := call(t, conn, scanner, 2, "route.add", map[string]any{"route": testRoute("/inv1")})
	if resp.Error != nil {
		t.Fatalf("route.add: %v", resp.Error)
	}

	// Delete route — should trigger invalidation.
	resp = call(t, conn, scanner, 3, "route.delete", map[string]string{"path": "/inv1"})
	if resp.Error != nil {
		t.Fatalf("route.delete: %v", resp.Error)
	}

	if n := invalidations.Load(); n != 2 {
		t.Errorf("invalidations = %d; want 2", n)
	}
}

// ── Lifecycle tests ───────────────────────────────────────────────────────────

func TestServe_returnsOnContextCancel(t *testing.T) {
	t.Parallel()

	sockPath := shortSockPath(t)
	cfg := fairway.SocketConfig{
		Path:    sockPath,
		Router:  newTestRouter(t),
		Version: "v1",
		Now:     time.Now,
	}
	ss := fairway.NewSocketServer(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ss.Serve(ctx) }()

	// Wait for socket to appear.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Serve did not return after context cancel")
	}
}

func TestServe_removesSocketFileOnShutdown(t *testing.T) {
	t.Parallel()

	sockPath := shortSockPath(t)
	cfg := fairway.SocketConfig{
		Path:    sockPath,
		Router:  newTestRouter(t),
		Version: "v1",
		Now:     time.Now,
	}
	ss := fairway.NewSocketServer(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ss.Serve(ctx) }()

	// Wait for socket.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	<-done

	if _, err := os.Stat(sockPath); err == nil {
		t.Error("socket file should be removed after shutdown")
	}
}

func TestServe_socketHasMode0600(t *testing.T) {
	t.Parallel()

	sockPath := startSocket(t, defaultSocketCfg("v1", newTestRouter(t), nil))

	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("socket perm = %04o; want 0600", perm)
	}
}
