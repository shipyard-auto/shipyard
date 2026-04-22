// Package daemon orchestrates the long-lived shipyard-crew service mode:
// PID file acquisition, runtime construction (agent + backend + tools +
// pool + runner), Unix-socket JSON-RPC server (Task 21) and graceful
// shutdown with a fixed 15s timeout.
//
// The package is consumed exclusively by addons/crew/cmd/main.go in service
// mode. It returns a numeric exit code paired with an optional error so the
// caller can report and exit deterministically.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/agent"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/backend"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/config"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/conversation"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/logs"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/pidfile"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/pool"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/runner"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/socket"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

// Exit codes returned by Run. They mirror the public contract documented in
// addons/crew/cmd/main.go.
const (
	ExitOK              = 0
	ExitAlreadyRunning  = 10
	ExitInvalidConfig   = 20
	ExitBuildRuntime    = 30
	ExitShutdownTimeout = 50
)

// DefaultShutdownTimeout is the fixed 15 second budget for graceful shutdown
// before the daemon forces close and reports ExitShutdownTimeout.
const DefaultShutdownTimeout = 15 * time.Second

// Options is the runtime configuration of one daemon instance.
type Options struct {
	// AgentName is the bare agent identifier (validated by the caller).
	AgentName string

	// AgentDir is the directory that holds agent.yaml and prompt.md.
	// Must be readable.
	AgentDir string

	// RunDir is the directory where the PID file and Unix socket live.
	// Created with 0700 if absent.
	RunDir string

	// ConfigPath optionally points at the global crew config.yaml. Empty
	// uses defaults from config.Default.
	ConfigPath string

	// LogDir is the directory for JSONL run logs. Empty uses the default
	// (<SHIPYARD_HOME>/logs/crew). The directory is created on demand by
	// the emitter.
	LogDir string

	// Version is the daemon version string used in handshake responses.
	Version string

	// ShutdownTimeout overrides the default 15s graceful shutdown budget.
	ShutdownTimeout time.Duration

	// Now is an injectable clock (defaults to time.Now). Used for status
	// uptime reporting.
	Now func() time.Time

	// SocketPathOverride and PIDPathOverride are escape hatches for tests
	// where macOS path-length limits force a non-default location.
	SocketPathOverride string
	PIDPathOverride    string
}

// Run executes the daemon lifecycle. It blocks until ctx is cancelled or the
// socket server reports a fatal error. Returns the exit code the caller
// should propagate plus the underlying error for logging.
func Run(ctx context.Context, opts Options) (int, error) {
	if opts.AgentName == "" {
		return ExitBuildRuntime, errors.New("daemon: agent name is required")
	}
	if opts.AgentDir == "" {
		return ExitBuildRuntime, errors.New("daemon: agent dir is required")
	}
	if opts.RunDir == "" {
		return ExitBuildRuntime, errors.New("daemon: run dir is required")
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = DefaultShutdownTimeout
	}

	if err := os.MkdirAll(opts.RunDir, 0o700); err != nil {
		return ExitBuildRuntime, fmt.Errorf("create run dir: %w", err)
	}

	pidPath := opts.PIDPathOverride
	if pidPath == "" {
		pidPath = filepath.Join(opts.RunDir, opts.AgentName+".pid")
	}
	sockPath := opts.SocketPathOverride
	if sockPath == "" {
		sockPath = filepath.Join(opts.RunDir, opts.AgentName+".sock")
	}

	pf, err := pidfile.Acquire(pidPath)
	if err != nil {
		var ar pidfile.ErrAlreadyRunning
		if errors.As(err, &ar) {
			return ExitAlreadyRunning, err
		}
		return ExitBuildRuntime, err
	}
	defer func() { _ = pf.Release() }()

	a, err := agent.Load(opts.AgentDir)
	if err != nil {
		return ExitInvalidConfig, fmt.Errorf("load agent: %w", err)
	}
	if a.Name != opts.AgentName {
		return ExitInvalidConfig, fmt.Errorf("agent name mismatch: agent.yaml=%q --agent=%q", a.Name, opts.AgentName)
	}

	rt, err := buildRuntime(a, opts)
	if err != nil {
		return ExitBuildRuntime, fmt.Errorf("build runtime: %w", err)
	}
	defer rt.closeLogs()

	innerCtx, cancelInner := context.WithCancel(ctx)
	defer cancelInner()

	deps := socket.Deps{
		AgentName:  opts.AgentName,
		Version:    opts.Version,
		Runner:     rt,
		Reload:     rt.reload,
		OnShutdown: cancelInner,
		Now:        opts.Now,
	}
	srv, err := socket.NewServer(sockPath, deps)
	if err != nil {
		return ExitBuildRuntime, fmt.Errorf("socket server: %w", err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(innerCtx) }()

	var fatalErr error
	select {
	case <-innerCtx.Done():
	case err := <-serveErr:
		if err != nil {
			fatalErr = err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ExitShutdownTimeout, fmt.Errorf("shutdown timeout after %s", opts.ShutdownTimeout)
		}
		return ExitBuildRuntime, fmt.Errorf("shutdown: %w", err)
	}

	// Drain Serve completion so the goroutine exits cleanly.
	select {
	case err := <-serveErr:
		if err != nil && fatalErr == nil {
			fatalErr = err
		}
	default:
	}
	if fatalErr != nil {
		return ExitBuildRuntime, fmt.Errorf("server: %w", fatalErr)
	}
	return ExitOK, nil
}

// runtime is the mutable wrapper around the runner and agent. It satisfies
// socket.Runner directly and exposes a reload entry point that swaps the
// agent definition under a write lock.
type runtime struct {
	mu       sync.RWMutex
	agentDir string
	rn       *runner.Runner
	em       logs.Emitter // owned by runtime; closed in closeLogs.
}

// closeLogs releases the file descriptor owned by the JSONL emitter.
// Safe to call when no emitter was attached.
func (r *runtime) closeLogs() {
	if r == nil || r.em == nil {
		return
	}
	_ = r.em.Close()
}

func (r *runtime) Run(ctx context.Context, p socket.RunParams) (socket.RunResult, error) {
	r.mu.RLock()
	rn := r.rn
	r.mu.RUnlock()

	in := runner.Input{
		Data:   p.Input,
		Source: "socket",
	}
	out, err := rn.Run(ctx, in)
	res := socket.RunResult{
		TraceID: out.TraceID,
		Text:    out.Text,
	}
	if out.Usage.InputTokens != 0 || out.Usage.OutputTokens != 0 {
		res.Data = map[string]any{
			"usage": map[string]int{
				"input_tokens":  out.Usage.InputTokens,
				"output_tokens": out.Usage.OutputTokens,
			},
		}
	}
	return res, err
}

func (r *runtime) reload(_ context.Context) error {
	a, err := agent.Load(r.agentDir)
	if err != nil {
		return fmt.Errorf("load agent: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rn.Agent = a
	return nil
}

func buildRuntime(a *crew.Agent, opts Options) (*runtime, error) {
	rn, err := NewRunner(a, opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	em, emErr := logs.NewFileEmitter(opts.LogDir)
	if emErr != nil {
		// Logs are best-effort; never block the daemon.
		em = logs.NewNopEmitter()
	}
	rn.Logs = logs.NewRunnerAdapter(em)
	return &runtime{rn: rn, agentDir: opts.AgentDir, em: em}, nil
}

// NewRunner wires the dependencies a crew runner needs (config, pool, backend,
// store, tool dispatcher) from a loaded agent and an optional config path.
// It is shared between service mode (daemon.Run) and on-demand mode.
func NewRunner(a *crew.Agent, configPath string) (*runner.Runner, error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	mgr := pool.NewManager(&cfg.Concurrency)

	if _, ok := cfg.Concurrency.Pools[a.Execution.Pool]; !ok {
		return nil, fmt.Errorf("agent.execution.pool %q not declared in config", a.Execution.Pool)
	}

	be, err := buildBackend(a)
	if err != nil {
		return nil, err
	}
	store, err := buildStore(a)
	if err != nil {
		return nil, err
	}
	disp := tools.NewDispatcher()

	return &runner.Runner{
		Agent:      a,
		Pool:       mgr,
		Store:      store,
		Backend:    be,
		Dispatcher: disp,
	}, nil
}

func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		return config.Default(), nil
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return config.Default(), nil
	}
	return config.Load(path)
}

func buildBackend(a *crew.Agent) (backend.Backend, error) {
	switch a.Backend.Type {
	case crew.BackendCLI:
		return backend.NewCLIBackend(), nil
	case crew.BackendAnthropicAPI:
		return backend.NewAPIBackend(), nil
	default:
		return nil, fmt.Errorf("unsupported backend type %q", a.Backend.Type)
	}
}

func buildStore(a *crew.Agent) (conversation.Store, error) {
	switch a.Conversation.Mode {
	case crew.ConversationStateless:
		return conversation.NewStateless(), nil
	case crew.ConversationStateful:
		return conversation.NewStateful(nil), nil
	default:
		return nil, fmt.Errorf("unsupported conversation mode %q", a.Conversation.Mode)
	}
}
