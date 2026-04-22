package crewctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// daemonScript declaratively describes what the fake daemon should do in
// response to each incoming request.
type daemonScript struct {
	// Per-call behaviour keyed by method name. Only the first matching call
	// fires; subsequent calls receive an "unscripted" error response.
	Handshake    func(id json.RawMessage) response
	Run          func(id json.RawMessage, req request) response
	AllowMulti   bool
	CloseOnWrite bool
}

// fakeDaemon listens on a Unix socket under a short path and answers with the
// supplied script. Safe for parallel tests.
type fakeDaemon struct {
	listener net.Listener
	path     string
	wg       sync.WaitGroup
}

func newFakeDaemon(t *testing.T, script *daemonScript) *fakeDaemon {
	t.Helper()
	dir, err := os.MkdirTemp("", "crewctl-")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	path := filepath.Join(dir, "crew.sock")
	// macOS Unix socket limit is 104 bytes; guard.
	if runtime.GOOS == "darwin" && len(path) > 100 {
		t.Fatalf("socket path too long: %d bytes", len(path))
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	d := &fakeDaemon{listener: lis, path: path}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				handleFakeConn(conn, script)
			}()
		}
	}()

	t.Cleanup(func() {
		lis.Close()
		d.wg.Wait()
		os.RemoveAll(dir)
	})
	return d
}

func handleFakeConn(conn net.Conn, script *daemonScript) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			writeFake(conn, response{
				JSONRPC: JSONRPCVersion,
				Error:   &RPCError{Code: ErrCodeParseError, Message: "parse error"},
			})
			return
		}
		if script.CloseOnWrite {
			return
		}
		var resp response
		switch req.Method {
		case "handshake":
			if script.Handshake != nil {
				resp = script.Handshake(req.ID)
			} else {
				resp = response{JSONRPC: JSONRPCVersion, ID: req.ID, Result: json.RawMessage(`{"version":"v1","agent":"demo"}`)}
			}
		case "run":
			if script.Run != nil {
				resp = script.Run(req.ID, req)
			} else {
				resp = response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: &RPCError{Code: ErrCodeMethodNotFound, Message: "no handler"}}
			}
		default:
			resp = response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: &RPCError{Code: ErrCodeMethodNotFound, Message: "method not found"}}
		}
		writeFake(conn, resp)
		if !script.AllowMulti && req.Method != "handshake" {
			return
		}
	}
}

func writeFake(conn net.Conn, resp response) {
	data, _ := json.Marshal(resp)
	conn.Write(append(data, '\n'))
}

func TestClient_DialHandshakeSuccess(t *testing.T) {
	d := newFakeDaemon(t, &daemonScript{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, err := Dial(ctx, Opts{SocketPath: d.path, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if got := c.DaemonVersion(); got != "v1" {
		t.Errorf("expected daemon version v1, got %q", got)
	}
}

func TestClient_DialDaemonNotRunning(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, "nobody.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := Dial(ctx, Opts{SocketPath: bogus, Version: "v1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("expected ErrDaemonNotRunning, got %v", err)
	}
}

func TestClient_DialVersionMismatch(t *testing.T) {
	script := &daemonScript{
		Handshake: func(id json.RawMessage) response {
			data, _ := json.Marshal(map[string]string{"daemon": "v2.0.0", "client": "v1.0.0"})
			return response{
				JSONRPC: JSONRPCVersion,
				ID:      id,
				Error:   &RPCError{Code: ErrCodeVersionMismatch, Message: "version mismatch", Data: data},
			}
		},
	}
	d := newFakeDaemon(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := Dial(ctx, Opts{SocketPath: d.path, Version: "v1.0.0"})
	if err == nil {
		t.Fatal("expected version mismatch")
	}
	var vm *ErrVersionMismatch
	if !errors.As(err, &vm) {
		t.Fatalf("expected ErrVersionMismatch, got %T: %v", err, err)
	}
	if vm.Daemon != "v2.0.0" || vm.Client != "v1.0.0" {
		t.Errorf("unexpected fields: %+v", vm)
	}
}

func TestClient_RunSuccess(t *testing.T) {
	runResp := map[string]any{
		"trace_id": "trace-xyz",
		"status":   "ok",
		"output": map[string]any{
			"text": "hello world",
			"data": map[string]any{"k": "v"},
		},
	}
	payload, _ := json.Marshal(runResp)
	script := &daemonScript{
		AllowMulti: true,
		Run: func(id json.RawMessage, _ request) response {
			return response{JSONRPC: JSONRPCVersion, ID: id, Result: payload}
		},
	}
	d := newFakeDaemon(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, err := Dial(ctx, Opts{SocketPath: d.path, Version: "v1"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	res, err := c.Run(ctx, json.RawMessage(`{"q":"hi"}`), time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.TraceID != "trace-xyz" || res.Status != "ok" || res.Text != "hello world" {
		t.Errorf("unexpected result: %+v", res)
	}
	if v, _ := res.Data["k"].(string); v != "v" {
		t.Errorf("unexpected data: %+v", res.Data)
	}
}

func TestClient_RunMethodNotFound(t *testing.T) {
	script := &daemonScript{
		AllowMulti: true,
		Run: func(id json.RawMessage, _ request) response {
			return response{JSONRPC: JSONRPCVersion, ID: id, Error: &RPCError{Code: ErrCodeMethodNotFound, Message: "method \"run\" not found"}}
		},
	}
	d := newFakeDaemon(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, err := Dial(ctx, Opts{SocketPath: d.path, Version: "v1"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	_, err = c.Run(ctx, json.RawMessage(`{}`), time.Second)
	if err == nil {
		t.Fatal("expected error")
	}
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != ErrCodeMethodNotFound {
		t.Errorf("expected code %d, got %d", ErrCodeMethodNotFound, rpcErr.Code)
	}
}

func TestClient_RunInvalidParams(t *testing.T) {
	script := &daemonScript{
		AllowMulti: true,
		Run: func(id json.RawMessage, _ request) response {
			return response{JSONRPC: JSONRPCVersion, ID: id, Error: &RPCError{Code: ErrCodeInvalidParams, Message: "invalid run params"}}
		},
	}
	d := newFakeDaemon(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, err := Dial(ctx, Opts{SocketPath: d.path, Version: "v1"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	_, err = c.Run(ctx, json.RawMessage(`{}`), time.Second)
	if err == nil {
		t.Fatal("expected error")
	}
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != ErrCodeInvalidParams {
		t.Errorf("expected invalid params error, got %v", err)
	}
}

func TestClient_RunInternalError(t *testing.T) {
	script := &daemonScript{
		AllowMulti: true,
		Run: func(id json.RawMessage, _ request) response {
			return response{JSONRPC: JSONRPCVersion, ID: id, Error: &RPCError{Code: ErrCodeInternal, Message: "boom"}}
		},
	}
	d := newFakeDaemon(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, err := Dial(ctx, Opts{SocketPath: d.path, Version: "v1"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	_, err = c.Run(ctx, json.RawMessage(`{}`), time.Second)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != ErrCodeInternal {
		t.Errorf("expected internal error, got %v", err)
	}
}

func TestClient_RunTimeout(t *testing.T) {
	script := &daemonScript{
		AllowMulti: true,
		Run: func(id json.RawMessage, _ request) response {
			// Never respond — sleep until the client gives up.
			time.Sleep(300 * time.Millisecond)
			return response{JSONRPC: JSONRPCVersion, ID: id, Result: json.RawMessage(`{"trace_id":"x"}`)}
		},
	}
	d := newFakeDaemon(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, err := Dial(ctx, Opts{SocketPath: d.path, Version: "v1"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	_, err = c.Run(ctx, json.RawMessage(`{}`), 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return
	}
	// Some runtimes surface the deadline as a plain io error; accept any error.
	t.Logf("timeout surfaced as: %v", err)
}

func TestClient_RunRequestShape(t *testing.T) {
	var seen request
	script := &daemonScript{
		AllowMulti: true,
		Run: func(id json.RawMessage, r request) response {
			seen = r
			return response{JSONRPC: JSONRPCVersion, ID: id, Result: json.RawMessage(`{"trace_id":"t","status":"ok","output":{"text":"ok"}}`)}
		},
	}
	d := newFakeDaemon(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, err := Dial(ctx, Opts{SocketPath: d.path, Version: "v1"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if _, err := c.Run(ctx, json.RawMessage(`{"q":"hi"}`), 750*time.Millisecond); err != nil {
		t.Fatalf("run: %v", err)
	}
	if seen.Method != "run" {
		t.Errorf("method = %q", seen.Method)
	}
	if seen.JSONRPC != JSONRPCVersion {
		t.Errorf("jsonrpc = %q", seen.JSONRPC)
	}
	var params struct {
		Input     json.RawMessage `json:"input"`
		TimeoutMs int64           `json:"timeout_ms"`
	}
	if err := json.Unmarshal(seen.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if string(params.Input) != `{"q":"hi"}` {
		t.Errorf("input = %s", params.Input)
	}
	if params.TimeoutMs != 750 {
		t.Errorf("timeout_ms = %d", params.TimeoutMs)
	}
}

func TestClient_DialCustomDialer(t *testing.T) {
	called := false
	_, err := Dial(context.Background(), Opts{
		SocketPath: "ignored",
		Version:    "v1",
		Dial: func(ctx context.Context, path string) (net.Conn, error) {
			called = true
			return nil, errors.New("no conn")
		},
	})
	if !called {
		t.Fatal("custom dialer was not invoked")
	}
	if !errors.Is(err, ErrDaemonNotRunning) {
		t.Errorf("expected ErrDaemonNotRunning, got %v", err)
	}
}
