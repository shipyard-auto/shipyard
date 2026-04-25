// shipyard-fairway is the HTTP gateway daemon for the Shipyard addon ecosystem.
// It exposes routes defined in routes.json as HTTP endpoints and executes
// configured actions (shipyard commands) when those endpoints are hit.
//
// CLI lives in the shipyard core (shipyard fairway <cmd>).
// This binary is daemon-only: no interactive CLI, only startup flags.
//
// See: addons/fairway/docs/product.md
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/app"
	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

const (
	exitOK             = 0
	exitFlagError      = 1
	exitAlreadyRunning = 10
	exitInvalidConfig  = 20
	exitFatalServer    = 40
	exitShutdownHang   = 50

	shutdownTimeout = 15 * time.Second
)

type pidReleaser interface {
	Release() error
}

type serverRunner interface {
	Serve(context.Context) error
	InFlight() int
}

type socketRunner interface {
	Serve(context.Context) error
}

type runDeps struct {
	args []string

	stdout io.Writer
	stderr io.Writer

	now func() time.Time

	defaultConfigPath func() (string, error)
	baseDir           func() (string, error)
	notifyContext     func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	mkdirAll          func(string, os.FileMode) error

	acquirePID      func(fairway.PIDFileOptions) (pidReleaser, error)
	newRepo         func(string) fairway.Repository
	newRouter       func(fairway.Repository) (*fairway.Router, error)
	newStats        func(time.Time) *fairway.Stats
	newExecutor     func(fairway.ExecutorConfig) fairway.Executor
	newServer       func(fairway.ServerConfig) serverRunner
	newSocketServer func(path string, router *fairway.Router, server serverRunner, stats *fairway.Stats, version string, now func() time.Time) socketRunner

	version      string
	versionInfo  func() string
	waitTimeout  time.Duration
	bootstrapLog *log.Logger
}

func main() {
	os.Exit(run(context.Background(), newRunDeps(os.Args[1:])))
}

func run(ctx context.Context, deps runDeps) int {
	deps = deps.withDefaults()

	fs := flag.NewFlagSet("shipyard-fairway", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		showVersion bool
		configPath  string
		logDir      string
	)

	fs.Usage = func() {
		fmt.Fprintf(deps.stdout, "Usage: shipyard-fairway [flags]\n\n")
		fmt.Fprintf(deps.stdout, "shipyard-fairway is the HTTP gateway daemon managed by the shipyard CLI.\n")
		fmt.Fprintf(deps.stdout, "Do not run this binary directly in production — use: shipyard fairway start\n\n")
		fmt.Fprintf(deps.stdout, "Flags:\n")
		fs.SetOutput(deps.stdout)
		fs.PrintDefaults()
		fs.SetOutput(io.Discard)
	}

	fs.BoolVar(&showVersion, "version", false, "print version information and exit")
	fs.StringVar(&configPath, "config", "", "path to config.json (default: ~/.shipyard/fairway/routes.json)")
	fs.StringVar(&logDir, "log-dir", "", "directory for request logs (default: ~/.shipyard/logs/fairway)")

	if err := fs.Parse(deps.args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.Usage()
			return exitOK
		}
		deps.bootstrapLog.Printf("bootstrap falhou: parse de flags: %v", err)
		return exitFlagError
	}

	if showVersion {
		fmt.Fprintln(deps.stdout, deps.versionInfo())
		return exitOK
	}

	resolvedConfigPath, resolvedLogDir, runDir, code, err := resolvePaths(configPath, logDir, deps)
	if err != nil {
		deps.bootstrapLog.Printf("bootstrap falhou: %v", err)
		return code
	}

	ctx, cancel := deps.notifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pidPath := filepath.Join(runDir, "fairway.pid")
	pid, err := deps.acquirePID(fairway.PIDFileOptions{Path: pidPath})
	if err != nil {
		var alreadyRunning fairway.ErrAlreadyRunning
		if errors.As(err, &alreadyRunning) {
			deps.bootstrapLog.Printf("bootstrap falhou: %v", err)
			return exitAlreadyRunning
		}
		deps.bootstrapLog.Printf("bootstrap falhou: acquire pidfile: %v", err)
		return exitFatalServer
	}
	defer func() { _ = pid.Release() }()

	repo := deps.newRepo(resolvedConfigPath)
	router, err := deps.newRouter(repo)
	if err != nil {
		deps.bootstrapLog.Printf("bootstrap falhou: carregar config: %v", err)
		return exitInvalidConfig
	}

	stats := deps.newStats(deps.now())
	executor := deps.newExecutor(fairway.ExecutorConfig{
		ShipyardBinary: resolveShipyardBinary(),
		MaxInFlight:    router.Config().MaxInFlight,
		Now:            deps.now,
	})

	daemonLogger := slog.New(slog.NewTextHandler(deps.stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Build the schema-v2 event logger that backs the HTTP middleware.
	// Resolves to the canonical ~/.shipyard/logs root so entries land at
	// <root>/fairway/YYYY-MM-DD.jsonl.
	logsRoot := filepath.Dir(resolvedLogDir)
	eventLogger := yardlogs.New(yardlogs.SourceFairway, yardlogs.Options{
		Store:   yardlogs.NewStore(logsRoot),
		Version: deps.version,
	})

	server := deps.newServer(fairway.ServerConfig{
		Router:      router,
		Executor:    executor,
		Logger:      daemonLogger,
		EventLogger: eventLogger,
		Stats:       stats,
	})

	socketPath := filepath.Join(runDir, "fairway.sock")
	socketServer := deps.newSocketServer(socketPath, router, server, stats, deps.version, deps.now)

	errCh := make(chan error, 2)
	go func() { errCh <- server.Serve(ctx) }()
	go func() { errCh <- socketServer.Serve(ctx) }()

	remaining := 2
	var fatalErr error

	select {
	case err := <-errCh:
		remaining--
		if err != nil {
			fatalErr = err
			deps.bootstrapLog.Printf("bootstrap falhou: servidor retornou erro fatal: %v", err)
			cancel()
		}
	case <-ctx.Done():
	}

	deadline := time.NewTimer(deps.waitTimeout)
	defer deadline.Stop()

	for remaining > 0 {
		select {
		case err := <-errCh:
			remaining--
			if fatalErr == nil && err != nil {
				fatalErr = err
				deps.bootstrapLog.Printf("bootstrap falhou: servidor retornou erro fatal: %v", err)
				cancel()
			}
		case <-deadline.C:
			deps.bootstrapLog.Printf("bootstrap falhou: shutdown excedeu timeout de %s", deps.waitTimeout)
			return exitShutdownHang
		}
	}

	if fatalErr != nil {
		return exitFatalServer
	}
	return exitOK
}

func resolvePaths(configPath, logDir string, deps runDeps) (string, string, string, int, error) {
	resolvedConfigPath := configPath
	if resolvedConfigPath == "" {
		p, err := deps.defaultConfigPath()
		if err != nil {
			return "", "", "", exitInvalidConfig, fmt.Errorf("resolver config path: %w", err)
		}
		resolvedConfigPath = p
	}

	baseDir, err := deps.baseDir()
	if err != nil {
		return "", "", "", exitFatalServer, fmt.Errorf("resolver base dir: %w", err)
	}

	resolvedLogDir := logDir
	if resolvedLogDir == "" {
		resolvedLogDir = filepath.Join(baseDir, "logs", "fairway")
	}

	runDir := filepath.Join(baseDir, "run")
	if err := deps.mkdirAll(runDir, 0700); err != nil {
		return "", "", "", exitFatalServer, fmt.Errorf("criar run dir %s: %w", runDir, err)
	}

	return resolvedConfigPath, resolvedLogDir, runDir, exitOK, nil
}

// resolveShipyardBinary returns the absolute path to the shipyard binary.
// It first looks next to the running fairway binary (both are installed in the
// same directory by shipyard fairway install). If that fails it falls back to
// exec.LookPath, which works when the caller's PATH is set correctly.
// Returns "shipyard" as a last resort so existing behaviour is preserved.
func resolveShipyardBinary() string {
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "shipyard")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if p, err := exec.LookPath("shipyard"); err == nil {
		return p
	}
	return "shipyard"
}

func newRunDeps(args []string) runDeps {
	return runDeps{args: args}
}

func (d runDeps) withDefaults() runDeps {
	if d.stdout == nil {
		d.stdout = os.Stdout
	}
	if d.stderr == nil {
		d.stderr = os.Stderr
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.defaultConfigPath == nil {
		d.defaultConfigPath = fairway.DefaultConfigPath
	}
	if d.baseDir == nil {
		d.baseDir = defaultBaseDir
	}
	if d.notifyContext == nil {
		d.notifyContext = signal.NotifyContext
	}
	if d.mkdirAll == nil {
		d.mkdirAll = os.MkdirAll
	}
	if d.acquirePID == nil {
		d.acquirePID = func(opts fairway.PIDFileOptions) (pidReleaser, error) {
			return fairway.Acquire(opts)
		}
	}
	if d.newRepo == nil {
		d.newRepo = func(path string) fairway.Repository {
			return fairway.NewFileRepositoryAt(path)
		}
	}
	if d.newRouter == nil {
		d.newRouter = fairway.NewRouter
	}
	if d.newStats == nil {
		d.newStats = fairway.NewStats
	}
	if d.newExecutor == nil {
		d.newExecutor = func(cfg fairway.ExecutorConfig) fairway.Executor {
			return fairway.NewExecutor(cfg)
		}
	}
	if d.newServer == nil {
		d.newServer = func(cfg fairway.ServerConfig) serverRunner {
			return fairway.NewServer(cfg)
		}
	}
	if d.newSocketServer == nil {
		d.newSocketServer = func(path string, router *fairway.Router, server serverRunner, stats *fairway.Stats, version string, now func() time.Time) socketRunner {
			realServer, ok := server.(*fairway.Server)
			if !ok {
				panic("newSocketServer requires *fairway.Server in production wiring")
			}
			return fairway.NewSocketServer(fairway.SocketConfig{
				Path:    path,
				Router:  router,
				Server:  realServer,
				Stats:   stats,
				Version: version,
				Now:     now,
			})
		}
	}
	if d.version == "" {
		d.version = app.Version
	}
	if d.versionInfo == nil {
		d.versionInfo = app.Info
	}
	if d.waitTimeout <= 0 {
		d.waitTimeout = shutdownTimeout
	}
	if d.bootstrapLog == nil {
		d.bootstrapLog = log.New(d.stderr, "", log.LstdFlags)
	}
	return d
}

func defaultBaseDir() (string, error) {
	if home := os.Getenv("SHIPYARD_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".shipyard"), nil
}
