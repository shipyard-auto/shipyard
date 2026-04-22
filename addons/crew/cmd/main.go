// shipyard-crew is the agent runtime binary for the Shipyard addon
// ecosystem. It has two operating modes:
//
//  1. On-demand (default): reads JSON from stdin, runs the agent once,
//     writes the run envelope to stdout and exits.
//  2. Service (--service): long-lived daemon that acquires a PID file,
//     binds a Unix socket (JSON-RPC 2.0) and handles RPC calls until
//     SIGTERM/SIGINT or a shutdown RPC.
//
// Exit code table (public contract — do not change without a major bump):
//
//	 0  Success
//	 1  Business failure (tool returned error, backend failed semantically)
//	 2  Invalid input (malformed stdin JSON or missing/invalid flag)
//	 3  Internal error while running on-demand mode
//	10  PID conflict (another live instance owns the PID file)
//	20  Failed to load agent.yaml
//	30  Failed to build runtime (config / pool / backend / store)
//	50  Graceful shutdown exceeded 15s budget
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/app"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/agent"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/daemon"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/runner"
)

const (
	ExitOK               = 0
	ExitBusinessFailure  = 1
	ExitInvalidInput     = 2
	ExitOnDemandInternal = 3
	ExitAlreadyRunning   = 10
	ExitInvalidConfig    = 20
	ExitBuildRuntime     = 30
	ExitShutdownTimeout  = 50
)

// maxOnDemandStdin caps the size of the JSON payload accepted on stdin in
// on-demand mode. 4 MiB is plenty for structured trigger inputs.
const maxOnDemandStdin = 4 << 20

var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

type runtimeDeps struct {
	Args         []string
	Env          func(string) string
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	Now          func() time.Time
	Exit         func(int)
	SignalCtx    func(parent context.Context) (context.Context, context.CancelFunc)
	RunService   func(ctx context.Context, opts daemon.Options) (int, error)
	RunOnDemand  func(ctx context.Context, req onDemandRequest) (int, error)
	RunReconcile func(ctx context.Context, req reconcileRequest) (int, error)
}

type onDemandRequest struct {
	AgentName  string
	AgentDir   string
	ConfigPath string
	Input      map[string]any
	Stdout     io.Writer
	Stderr     io.Writer
}

func main() {
	deps := runtimeDeps{
		Args:   os.Args[1:],
		Env:    os.Getenv,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Now:    time.Now,
		Exit:   os.Exit,
		SignalCtx: func(p context.Context) (context.Context, context.CancelFunc) {
			return signal.NotifyContext(p, syscall.SIGTERM, syscall.SIGINT)
		},
	}
	deps.Exit(run(context.Background(), deps))
}

func run(ctx context.Context, deps runtimeDeps) int {
	deps = deps.withDefaults()

	if len(deps.Args) > 0 && deps.Args[0] == "reconcile" {
		return runReconcileMode(ctx, deps, deps.Args[1:])
	}

	fs := flag.NewFlagSet("shipyard-crew", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		showVersion bool
		configPath  string
		logDir      string
		agentName   string
		serviceMode bool
	)

	fs.BoolVar(&showVersion, "version", false, "print version information and exit")
	fs.StringVar(&configPath, "config", "", "path to agent config.yaml (default: <SHIPYARD_HOME>/crew/config.yaml)")
	fs.StringVar(&logDir, "log-dir", "", "directory for agent logs (default: <SHIPYARD_HOME>/logs/crew)")
	fs.StringVar(&agentName, "agent", "", "agent name (required)")
	fs.BoolVar(&serviceMode, "service", false, "run as a managed service daemon")

	if err := fs.Parse(deps.Args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(deps.Stdout)
			fs.Usage()
			return ExitOK
		}
		fmt.Fprintf(deps.Stderr, "shipyard-crew: %s\n", err)
		return ExitInvalidInput
	}

	if showVersion {
		fmt.Fprintln(deps.Stdout, app.Info())
		return ExitOK
	}

	if !agentNameRe.MatchString(agentName) {
		fmt.Fprintln(deps.Stderr, "shipyard-crew: invalid --agent: must match ^[a-z0-9][a-z0-9_-]{0,62}$")
		return ExitInvalidInput
	}

	home := deps.Env("SHIPYARD_HOME")
	if home == "" {
		u, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(deps.Stderr, "shipyard-crew: %s\n", err)
			return ExitInvalidConfig
		}
		home = filepath.Join(u, ".shipyard")
	}

	if configPath == "" {
		configPath = filepath.Join(home, "crew", "config.yaml")
	}
	if logDir == "" {
		logDir = filepath.Join(home, "logs", "crew")
	}
	_ = logDir

	agentDir := filepath.Join(home, "crew", agentName)
	runDir := filepath.Join(home, "run", "crew")

	if serviceMode {
		return runServiceMode(ctx, deps, daemon.Options{
			AgentName:  agentName,
			AgentDir:   agentDir,
			RunDir:     runDir,
			ConfigPath: configPath,
			Version:    app.Version,
		})
	}

	return runOnDemandMode(ctx, deps, onDemandRequest{
		AgentName:  agentName,
		AgentDir:   agentDir,
		ConfigPath: configPath,
		Stdout:     deps.Stdout,
		Stderr:     deps.Stderr,
	})
}

func runServiceMode(parent context.Context, deps runtimeDeps, opts daemon.Options) int {
	sigCtx, cancel := deps.SignalCtx(parent)
	defer cancel()
	code, err := deps.RunService(sigCtx, opts)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard-crew: %s\n", err)
	}
	return code
}

func runOnDemandMode(parent context.Context, deps runtimeDeps, req onDemandRequest) int {
	raw, err := readStdinLimit(deps.Stdin, maxOnDemandStdin)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard-crew: stdin: %s\n", err)
		return ExitInvalidInput
	}

	input := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			fmt.Fprintf(deps.Stderr, "shipyard-crew: invalid stdin json: %s\n", err)
			return ExitInvalidInput
		}
	}

	req.Input = input

	sigCtx, cancel := deps.SignalCtx(parent)
	defer cancel()

	code, err := deps.RunOnDemand(sigCtx, req)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard-crew: %s\n", err)
	}
	return code
}

// defaultRunOnDemand loads the agent, builds the runner and executes a single
// turn, emitting the envelope to req.Stdout.
func defaultRunOnDemand(ctx context.Context, req onDemandRequest) (int, error) {
	a, err := agent.Load(req.AgentDir)
	if err != nil {
		return ExitInvalidConfig, fmt.Errorf("load agent: %w", err)
	}
	if a.Name != req.AgentName {
		return ExitInvalidConfig, fmt.Errorf("agent name mismatch: agent.yaml=%q --agent=%q", a.Name, req.AgentName)
	}
	rn, err := daemon.NewRunner(a, req.ConfigPath)
	if err != nil {
		return ExitBuildRuntime, fmt.Errorf("build runtime: %w", err)
	}

	out, runErr := rn.Run(ctx, runner.Input{Data: req.Input, Source: "on-demand"})
	env := map[string]any{
		"trace_id": out.TraceID,
		"output": map[string]any{
			"text": out.Text,
		},
	}
	if runErr != nil {
		env["status"] = "error"
		env["error"] = runErr.Error()
		if err := writeEnvelope(req.Stdout, env); err != nil {
			return ExitOnDemandInternal, fmt.Errorf("write envelope: %w", err)
		}
		return ExitBusinessFailure, nil
	}
	env["status"] = "ok"
	if err := writeEnvelope(req.Stdout, env); err != nil {
		return ExitOnDemandInternal, fmt.Errorf("write envelope: %w", err)
	}
	return ExitOK, nil
}

func writeEnvelope(w io.Writer, env map[string]any) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

func readStdinLimit(r io.Reader, max int) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	lr := io.LimitReader(r, int64(max)+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		return nil, fmt.Errorf("stdin exceeds %d bytes", max)
	}
	return data, nil
}

func (d runtimeDeps) withDefaults() runtimeDeps {
	if d.Env == nil {
		d.Env = os.Getenv
	}
	if d.Stdin == nil {
		d.Stdin = os.Stdin
	}
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Exit == nil {
		d.Exit = os.Exit
	}
	if d.SignalCtx == nil {
		d.SignalCtx = func(p context.Context) (context.Context, context.CancelFunc) {
			return signal.NotifyContext(p, syscall.SIGTERM, syscall.SIGINT)
		}
	}
	if d.RunService == nil {
		d.RunService = daemon.Run
	}
	if d.RunOnDemand == nil {
		d.RunOnDemand = defaultRunOnDemand
	}
	if d.RunReconcile == nil {
		d.RunReconcile = defaultRunReconcile
	}
	return d
}
