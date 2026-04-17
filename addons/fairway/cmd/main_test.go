package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

type fakePID struct {
	released bool
}

func (p *fakePID) Release() error {
	p.released = true
	return nil
}

type fakeServer struct {
	serve    func(context.Context) error
	inFlight int
}

func (s *fakeServer) Serve(ctx context.Context) error {
	if s.serve != nil {
		return s.serve(ctx)
	}
	<-ctx.Done()
	return nil
}

func (s *fakeServer) InFlight() int { return s.inFlight }

type fakeSocket struct {
	serve func(context.Context) error
}

func (s *fakeSocket) Serve(ctx context.Context) error {
	if s.serve != nil {
		return s.serve(ctx)
	}
	<-ctx.Done()
	return nil
}

func testRouter(t *testing.T) *fairway.Router {
	t.Helper()
	cfg := fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          fairway.DefaultPort,
		Bind:          fairway.DefaultBind,
		Routes:        []fairway.Route{},
	}
	repo := fairway.NewFileRepositoryAt(filepath.Join(t.TempDir(), "routes.json"))
	return fairway.NewRouterWithConfig(repo, cfg)
}

func unitDeps(t *testing.T) (runDeps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	router := testRouter(t)
	pid := &fakePID{}
	srv := &fakeServer{}
	sock := &fakeSocket{}

	deps := runDeps{
		args:   nil,
		stdout: stdout,
		stderr: stderr,
		now: func() time.Time {
			return time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
		},
		defaultConfigPath: func() (string, error) { return filepath.Join(t.TempDir(), "routes.json"), nil },
		baseDir:           func() (string, error) { return t.TempDir(), nil },
		notifyContext: func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(parent)
			cancel()
			return ctx, func() {}
		},
		mkdirAll: func(string, os.FileMode) error { return nil },
		acquirePID: func(fairway.PIDFileOptions) (pidReleaser, error) {
			return pid, nil
		},
		newRepo: func(string) fairway.Repository {
			return fairway.NewFileRepositoryAt(filepath.Join(t.TempDir(), "routes.json"))
		},
		newRouter: func(fairway.Repository) (*fairway.Router, error) {
			return router, nil
		},
		newRequestLogger: fairway.NewRequestLogger,
		newStats:         fairway.NewStats,
		newExecutor: func(cfg fairway.ExecutorConfig) fairway.Executor {
			return fairway.NewExecutor(cfg)
		},
		newServer: func(fairway.ServerConfig) serverRunner { return srv },
		newSocketServer: func(string, *fairway.Router, serverRunner, *fairway.Stats, string, func() time.Time) socketRunner {
			return sock
		},
		version:     "test-version",
		versionInfo: func() string { return "shipyard-fairway test-version (deadbeef, built now)" },
		waitTimeout: 50 * time.Millisecond,
	}

	return deps, stdout, stderr
}

func TestRun_versionFlagPrintsVersion_exit0(t *testing.T) {
	t.Parallel()

	deps, stdout, _ := unitDeps(t)
	deps.args = []string{"--version"}
	code := run(context.Background(), deps)

	if code != exitOK {
		t.Fatalf("run() = %d; want 0", code)
	}
	if !strings.Contains(stdout.String(), "shipyard-fairway test-version") {
		t.Fatalf("stdout = %q; want version string", stdout.String())
	}
}

func TestRun_helpFlagExit0(t *testing.T) {
	t.Parallel()

	deps, stdout, _ := unitDeps(t)
	deps.args = []string{"--help"}
	code := run(context.Background(), deps)

	if code != exitOK {
		t.Fatalf("run() = %d; want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage: shipyard-fairway") {
		t.Fatalf("stdout = %q; want usage", stdout.String())
	}
}

func TestRun_invalidFlag_exit1(t *testing.T) {
	t.Parallel()

	deps, _, stderr := unitDeps(t)
	deps.args = []string{"--bogus"}
	code := run(context.Background(), deps)

	if code != exitFlagError {
		t.Fatalf("run() = %d; want %d", code, exitFlagError)
	}
	if !strings.Contains(stderr.String(), "bootstrap falhou") {
		t.Fatalf("stderr = %q; want bootstrap failure", stderr.String())
	}
}

func TestRun_alreadyRunning_exit10(t *testing.T) {
	t.Parallel()

	deps, _, stderr := unitDeps(t)
	deps.acquirePID = func(fairway.PIDFileOptions) (pidReleaser, error) {
		return nil, fairway.ErrAlreadyRunning{PID: 1234}
	}

	code := run(context.Background(), deps)
	if code != exitAlreadyRunning {
		t.Fatalf("run() = %d; want %d", code, exitAlreadyRunning)
	}
	if !strings.Contains(stderr.String(), "bootstrap falhou") {
		t.Fatalf("stderr = %q; want bootstrap failure", stderr.String())
	}
}

func TestRun_invalidConfig_exit20(t *testing.T) {
	t.Parallel()

	deps, _, stderr := unitDeps(t)
	deps.newRouter = func(fairway.Repository) (*fairway.Router, error) {
		return nil, fmt.Errorf("invalid config: %w", fairway.ErrUnsupportedSchema)
	}

	code := run(context.Background(), deps)
	if code != exitInvalidConfig {
		t.Fatalf("run() = %d; want %d", code, exitInvalidConfig)
	}
	if !strings.Contains(stderr.String(), "carregar config") {
		t.Fatalf("stderr = %q; want config failure", stderr.String())
	}
}

func TestRun_loggerInit_exit30(t *testing.T) {
	t.Parallel()

	deps, _, stderr := unitDeps(t)
	deps.newRequestLogger = func(string, func() time.Time) (*fairway.RequestLogger, error) {
		return nil, errors.New("logger boom")
	}

	code := run(context.Background(), deps)
	if code != exitLoggerInit {
		t.Fatalf("run() = %d; want %d", code, exitLoggerInit)
	}
	if !strings.Contains(stderr.String(), "inicializar logger") {
		t.Fatalf("stderr = %q; want logger failure", stderr.String())
	}
}

func TestRun_serverFatal_exit40(t *testing.T) {
	t.Parallel()

	deps, _, stderr := unitDeps(t)
	srv := &fakeServer{
		serve: func(context.Context) error { return errors.New("server boom") },
	}
	sock := &fakeSocket{
		serve: func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		},
	}
	deps.newServer = func(fairway.ServerConfig) serverRunner { return srv }
	deps.newSocketServer = func(string, *fairway.Router, serverRunner, *fairway.Stats, string, func() time.Time) socketRunner {
		return sock
	}
	deps.notifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}

	code := run(context.Background(), deps)
	if code != exitFatalServer {
		t.Fatalf("run() = %d; want %d", code, exitFatalServer)
	}
	if !strings.Contains(stderr.String(), "servidor retornou erro fatal") {
		t.Fatalf("stderr = %q; want fatal server message", stderr.String())
	}
}

func TestRun_shutdownTimeout_exit50(t *testing.T) {
	t.Parallel()

	deps, _, stderr := unitDeps(t)
	blockLongerThanTimeout := func(context.Context) error {
		<-time.After(time.Second)
		return nil
	}
	deps.newServer = func(fairway.ServerConfig) serverRunner {
		return &fakeServer{serve: blockLongerThanTimeout}
	}
	deps.newSocketServer = func(string, *fairway.Router, serverRunner, *fairway.Stats, string, func() time.Time) socketRunner {
		return &fakeSocket{serve: blockLongerThanTimeout}
	}

	code := run(context.Background(), deps)
	if code != exitShutdownHang {
		t.Fatalf("run() = %d; want %d", code, exitShutdownHang)
	}
	if !strings.Contains(stderr.String(), "shutdown excedeu timeout") {
		t.Fatalf("stderr = %q; want shutdown timeout", stderr.String())
	}
}

func TestRun_missingConfigPathAndCanceledContext_exit0(t *testing.T) {
	t.Parallel()

	deps, _, stderr := unitDeps(t)
	deps.args = []string{"--config", filepath.Join(t.TempDir(), "does-not-exist.json")}
	code := run(context.Background(), deps)

	if code != exitOK {
		t.Fatalf("run() = %d; want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q; want empty", stderr.String())
	}
}

func TestResolvePaths_defaults(t *testing.T) {
	t.Parallel()

	deps, _, _ := unitDeps(t)
	base := t.TempDir()
	deps.baseDir = func() (string, error) { return base, nil }
	deps.defaultConfigPath = func() (string, error) { return filepath.Join(base, "fairway", "routes.json"), nil }

	configPath, logDir, runDir, code, err := resolvePaths("", "", deps)
	if err != nil {
		t.Fatalf("resolvePaths() error: %v", err)
	}
	if code != exitOK {
		t.Fatalf("resolvePaths() code = %d; want 0", code)
	}
	if configPath != filepath.Join(base, "fairway", "routes.json") {
		t.Fatalf("configPath = %q", configPath)
	}
	if logDir != filepath.Join(base, "logs", "fairway") {
		t.Fatalf("logDir = %q", logDir)
	}
	if runDir != filepath.Join(base, "run") {
		t.Fatalf("runDir = %q", runDir)
	}
}

func TestDefaultBaseDir_respectsShipyardHome(t *testing.T) {
	t.Setenv("SHIPYARD_HOME", "/tmp/shipyard-home")

	got, err := defaultBaseDir()
	if err != nil {
		t.Fatalf("defaultBaseDir() error: %v", err)
	}
	if got != "/tmp/shipyard-home" {
		t.Fatalf("defaultBaseDir() = %q; want %q", got, "/tmp/shipyard-home")
	}
}
