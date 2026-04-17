package fairway

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewDaemon(t *testing.T) {
	cfg := Config{
		SchemaVersion: SchemaVersion,
		Port:          DefaultPort,
		Bind:          DefaultBind,
		Routes:        []Route{},
	}

	t.Run("BuildsDaemonFromDefaults", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("SHIPYARD_HOME", root)

		repo := NewFileRepositoryAt(filepath.Join(root, "fairway", "routes.json"))
		if err := repo.Save(cfg); err != nil {
			t.Fatalf("repo.Save() error = %v", err)
		}

		daemon, err := NewDaemon(BootstrapConfig{Version: "0.22.0"})
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}
		if daemon.socketPath != filepath.Join(root, "run", "fairway.sock") {
			t.Fatalf("socketPath = %q", daemon.socketPath)
		}
		if daemon.pidfile.Path() != filepath.Join(root, "run", "fairway.pid") {
			t.Fatalf("pidfile path = %q", daemon.pidfile.Path())
		}
		status := daemon.Status()
		if status.ConfigPath != repo.Path() {
			t.Fatalf("status.ConfigPath = %q, want %q", status.ConfigPath, repo.Path())
		}
		if status.SocketPath != filepath.Join(root, "run", "fairway.sock") {
			t.Fatalf("status.SocketPath = %q", status.SocketPath)
		}
	})

	t.Run("FailsWhenConfigIsInvalid", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("SHIPYARD_HOME", root)

		repo := NewFileRepositoryAt(filepath.Join(root, "fairway", "routes.json"))
		if err := os.MkdirAll(filepath.Dir(repo.Path()), 0o700); err != nil {
			t.Fatalf("os.MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(repo.Path(), []byte(`{"schemaVersion":"999","port":9876,"bind":"127.0.0.1","routes":[]}`), 0o600); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		if _, err := NewDaemon(BootstrapConfig{}); err == nil {
			t.Fatal("NewDaemon() error = nil, want error")
		}
	})
}

func TestRuntimeStatus(t *testing.T) {
	cfg := Config{
		SchemaVersion: SchemaVersion,
		Port:          DefaultPort,
		Bind:          DefaultBind,
		Routes: []Route{
			{Path: "/hooks/a", Auth: Auth{Type: AuthLocalOnly}, Action: Action{Type: ActionMessageSend}},
			{Path: "/hooks/b", Auth: Auth{Type: AuthLocalOnly}, Action: Action{Type: ActionTelegramHandle}},
		},
	}

	status := newRuntimeStatus(cfg, "1.2.3", "/tmp/routes.json", "/tmp/fairway.sock", "/tmp/fairway.pid")
	snapshot := status.Status()
	if snapshot.RouteCount != 2 {
		t.Fatalf("RouteCount = %d, want 2", snapshot.RouteCount)
	}
	if snapshot.PID != os.Getpid() {
		t.Fatalf("PID = %d, want %d", snapshot.PID, os.Getpid())
	}

	startedAt := time.Date(2026, 4, 17, 1, 45, 0, 0, time.FixedZone("x", -3*3600))
	status.MarkStarted(startedAt)
	snapshot = status.Status()
	if snapshot.StartedAt != startedAt.UTC() {
		t.Fatalf("StartedAt = %s, want %s", snapshot.StartedAt, startedAt.UTC())
	}

	status.MarkStopped()
	if got := status.Status().StartedAt; !got.IsZero() {
		t.Fatalf("StartedAt after stop = %s, want zero", got)
	}
}

func TestDaemonRunLifecycle(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "run", "fairway.sock")

	t.Run("ShutsDownOnContextCancellation", func(t *testing.T) {
		httpServer := newFakeHTTPDaemon()
		socketServer := newFakeSocketDaemon()
		pidfile := &fakePIDFile{path: filepath.Join(t.TempDir(), "fairway.pid")}

		daemon, err := newDaemon(httpServer, socketServer, pidfile, socketPath, 250*time.Millisecond)
		if err != nil {
			t.Fatalf("newDaemon() error = %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- daemon.Run(ctx) }()

		httpServer.waitStarted(t)
		socketServer.waitStarted(t)
		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run() did not return")
		}

		if !pidfile.acquired || !pidfile.released {
			t.Fatalf("pidfile lifecycle = acquire %v release %v, want both true", pidfile.acquired, pidfile.released)
		}
		if !httpServer.shutdownCalled {
			t.Fatal("http shutdown not called")
		}
		if !socketServer.shutdownCalled {
			t.Fatal("socket shutdown not called")
		}
	})

	t.Run("ReturnsServerErrors", func(t *testing.T) {
		httpServer := newFakeHTTPDaemon()
		socketServer := newFakeSocketDaemon()
		pidfile := &fakePIDFile{path: filepath.Join(t.TempDir(), "fairway.pid")}

		daemon, err := newDaemon(httpServer, socketServer, pidfile, socketPath, time.Second)
		if err != nil {
			t.Fatalf("newDaemon() error = %v", err)
		}

		done := make(chan error, 1)
		go func() { done <- daemon.Run(context.Background()) }()

		httpServer.waitStarted(t)
		httpServer.errCh <- errors.New("http serve failed")

		select {
		case err := <-done:
			if err == nil || err.Error() != "http serve failed" {
				t.Fatalf("Run() error = %v, want http serve failed", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run() did not return")
		}
	})

	t.Run("CleansUpWhenHTTPStartFails", func(t *testing.T) {
		httpServer := newFakeHTTPDaemon()
		httpServer.startErr = errors.New("bind failed")
		socketServer := newFakeSocketDaemon()
		pidfile := &fakePIDFile{path: filepath.Join(t.TempDir(), "fairway.pid")}

		daemon, err := newDaemon(httpServer, socketServer, pidfile, socketPath, time.Second)
		if err != nil {
			t.Fatalf("newDaemon() error = %v", err)
		}

		err = daemon.Run(context.Background())
		if err == nil || err.Error() != "bind failed" {
			t.Fatalf("Run() error = %v, want bind failed", err)
		}
		if !socketServer.shutdownCalled {
			t.Fatal("socket shutdown not called after http start failure")
		}
		if !pidfile.released {
			t.Fatal("pidfile not released after http start failure")
		}
	})
}

func TestPrepareSocketPath(t *testing.T) {
	t.Run("CreatesRuntimeDirectory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "run", "fairway.sock")
		if err := prepareSocketPath(path); err != nil {
			t.Fatalf("prepareSocketPath() error = %v", err)
		}

		info, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("os.Stat() error = %v", err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("dir mode = %o, want 700", got)
		}
	})

	t.Run("RemovesStaleSocket", func(t *testing.T) {
		dir, err := os.MkdirTemp("/tmp", "fairway-daemon-sock-*")
		if err != nil {
			t.Fatalf("os.MkdirTemp() error = %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		path := filepath.Join(dir, "fairway.sock")

		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatalf("net.Listen() error = %v", err)
		}
		listener.Close()

		if err := prepareSocketPath(path); err != nil {
			t.Fatalf("prepareSocketPath() error = %v", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("os.Stat(%q) error = %v, want not exist", path, err)
		}
	})

	t.Run("RejectsNonSocketFile", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway.sock")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		if err := prepareSocketPath(path); err == nil {
			t.Fatal("prepareSocketPath() error = nil, want error")
		}
	})
}

type fakeHTTPDaemon struct {
	startErr       error
	shutdownErr    error
	errCh          chan error
	started        chan struct{}
	shutdownCalled bool
}

func newFakeHTTPDaemon() *fakeHTTPDaemon {
	return &fakeHTTPDaemon{
		errCh:   make(chan error, 1),
		started: make(chan struct{}),
	}
}

func (f *fakeHTTPDaemon) Start() error {
	if f.startErr != nil {
		return f.startErr
	}
	select {
	case <-f.started:
	default:
		close(f.started)
	}
	return nil
}

func (f *fakeHTTPDaemon) Shutdown(context.Context) error {
	f.shutdownCalled = true
	return f.shutdownErr
}

func (f *fakeHTTPDaemon) Errors() <-chan error { return f.errCh }

func (f *fakeHTTPDaemon) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(2 * time.Second):
		t.Fatal("http daemon did not start")
	}
}

type fakeSocketDaemon struct {
	startErr       error
	shutdownErr    error
	errCh          chan error
	started        chan struct{}
	shutdownCalled bool
	startPath      string
}

func newFakeSocketDaemon() *fakeSocketDaemon {
	return &fakeSocketDaemon{
		errCh:   make(chan error, 1),
		started: make(chan struct{}),
	}
}

func (f *fakeSocketDaemon) Start(path string) error {
	f.startPath = path
	if f.startErr != nil {
		return f.startErr
	}
	select {
	case <-f.started:
	default:
		close(f.started)
	}
	return nil
}

func (f *fakeSocketDaemon) Shutdown() error {
	f.shutdownCalled = true
	return f.shutdownErr
}

func (f *fakeSocketDaemon) Errors() <-chan error { return f.errCh }

func (f *fakeSocketDaemon) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(2 * time.Second):
		t.Fatal("socket daemon did not start")
	}
}

type fakePIDFile struct {
	path       string
	acquireErr error
	releaseErr error
	acquired   bool
	released   bool
}

func (f *fakePIDFile) Acquire() error {
	if f.acquireErr != nil {
		return f.acquireErr
	}
	f.acquired = true
	return nil
}

func (f *fakePIDFile) Release() error {
	f.released = true
	return f.releaseErr
}

func (f *fakePIDFile) Path() string { return f.path }
