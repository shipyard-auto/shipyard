package wa

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startRPC serves h's Outbox on a socket in a short temp dir (unix socket
// paths are limited to ~104 bytes, which t.TempDir can exceed on macOS).
func startRPC(t *testing.T, h *outboxHarness) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wa-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "wa.sock")
	ln, err := ListenRPC(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := &RPCServer{Outbox: h.ob, Tracker: NewTracker("", 42, nil), Log: h.ob.Log}
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return sock
}

func TestRPCSendStatusAndErrors(t *testing.T) {
	h := newOutbox(t)
	sock := startRPC(t, h)
	ctx := context.Background()

	info, err := os.Stat(sock)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket perm: %v %v", info, err)
	}

	var res SendResult
	if err := CallRPC(ctx, sock, "send", SendRequest{Chat: viniJID.String(), Text: "oi"}, &res); err != nil {
		t.Fatal(err)
	}
	if res.MsgID != "MSG1" || res.ChatName != "Vini Eduqo" {
		t.Fatalf("result=%+v", res)
	}

	h.cfg.Paused = true
	err = CallRPC(ctx, sock, "send", SendRequest{Chat: viniJID.String(), Text: "oi"}, &res)
	var re *RPCError
	if !errors.As(err, &re) || re.SendError() == nil || re.SendError().Code != CodePaused || re.Code != -32010 {
		t.Fatalf("refusal over rpc: %#v", err)
	}

	var st Status
	if err := CallRPC(ctx, sock, "status", nil, &st); err != nil || st.PID != 42 {
		t.Fatalf("status=%+v err=%v", st, err)
	}
	if err := CallRPC(ctx, sock, "nope", nil, nil); !errors.As(err, &re) || re.Code != rpcMethodNotFound {
		t.Fatalf("unknown method: %v", err)
	}

	// Malformed params are a typed invalid refusal, not a crash.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("{not json\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), `"code":-32700`) {
		t.Fatalf("parse error reply: %q", buf[:n])
	}
}

func TestCallRPCWithoutDaemon(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "absent.sock")
	if err := CallRPC(context.Background(), sock, "status", nil, nil); !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("err=%v", err)
	}
}

func TestListenRPCReplacesStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "wa-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sock := filepath.Join(dir, "wa.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil { // left by a crash
		t.Fatal(err)
	}
	ln, err := ListenRPC(sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
}

func TestListenRPCRejectsOverlongPath(t *testing.T) {
	long := "/tmp/" + strings.Repeat("x", 120) + "/wa.sock"
	if _, err := ListenRPC(long); err == nil || !strings.Contains(err.Error(), "shorter SHIPYARD_HOME") {
		t.Fatalf("err=%v", err)
	}
}

func TestServeShutdownClosesIdleConnections(t *testing.T) {
	h := newOutbox(t)
	dir, err := os.MkdirTemp("/tmp", "wa-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ln, err := ListenRPC(filepath.Join(dir, "wa.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		(&RPCServer{Outbox: h.ob, Tracker: NewTracker("", 1, nil), Log: h.ob.Log}).Serve(ctx, ln)
		close(done)
	}()
	idle, err := net.Dial("unix", filepath.Join(dir, "wa.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idle.Close() }()
	time.Sleep(50 * time.Millisecond) // let the server accept it
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return with an idle client connected")
	}
}
