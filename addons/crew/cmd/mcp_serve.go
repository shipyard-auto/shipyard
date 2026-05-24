package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/app"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/agent"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/backend"
	cwlogs "github.com/shipyard-auto/shipyard/addons/crew/internal/crew/logs"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/mcpserver"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// toolCallEmitter is the subset of the runner.Emitter interface that
// dispatcherHandler relies on. Kept narrow so tests can inject a fake
// without dragging the whole adapter.
type toolCallEmitter interface {
	ToolCallStart(ctx context.Context, agent *crew.Agent, traceID, tool string, input map[string]any)
	ToolCallEnd(ctx context.Context, agent *crew.Agent, traceID, tool string, env tools.Envelope, err error)
}

// Exit codes for the `mcp-serve` subcommand. Share the general addon table
// where the semantics match (2 invalid input, 20 failed to load agent) and
// add ExitOnDemandInternal for transport-level failures inside Serve.

type mcpServeRequest struct {
	AgentName string
	AgentDir  string
	// LogsDir is the parent directory that hosts <source>/YYYY-MM-DD.jsonl
	// (i.e. <SHIPYARD_HOME>/logs). Empty disables tool-call logging — the
	// serve loop still runs, but dispatcherHandler does not emit records.
	LogsDir string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

// runMCPServeMode parses flags for `mcp-serve` and dispatches to the
// injectable handler. The subcommand is the server side of the MCP stdio
// transport: Claude Code spawns us with --mcp-config pointing at a JSON
// file that names the `shipyard-crew mcp-serve --agent <name>` command.
func runMCPServeMode(parent context.Context, deps runtimeDeps, args []string) int {
	fs := flag.NewFlagSet("shipyard-crew mcp-serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var agentName string
	fs.StringVar(&agentName, "agent", "", "agent name (required)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(deps.Stdout)
			fs.Usage()
			return ExitOK
		}
		fmt.Fprintf(deps.Stderr, "shipyard-crew mcp-serve: %s\n", err)
		return ExitInvalidInput
	}

	if !agentNameRe.MatchString(agentName) {
		fmt.Fprintln(deps.Stderr, "shipyard-crew mcp-serve: invalid --agent: must match ^[a-z0-9][a-z0-9_-]{0,62}$")
		return ExitInvalidInput
	}

	home := deps.Env("SHIPYARD_HOME")
	if home == "" {
		u, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(deps.Stderr, "shipyard-crew mcp-serve: %s\n", err)
			return ExitInvalidConfig
		}
		home = filepath.Join(u, ".shipyard")
	}

	req := mcpServeRequest{
		AgentName: agentName,
		AgentDir:  filepath.Join(home, "crew", agentName),
		LogsDir:   filepath.Join(home, "logs"),
		Stdin:     deps.Stdin,
		Stdout:    deps.Stdout,
		Stderr:    deps.Stderr,
	}

	// MCP server MUST respect context cancellation so Claude Code can close
	// us cleanly on its own shutdown. We reuse SignalCtx for that.
	sigCtx, cancel := deps.SignalCtx(parent)
	defer cancel()

	code, err := deps.RunMCPServe(sigCtx, req)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard-crew mcp-serve: %s\n", err)
	}
	return code
}

// defaultRunMCPServe is the production mcp-serve handler. It loads the
// agent, constructs a dispatcher (exec + http drivers), wires a Handler
// adapter and runs mcpserver.Server.Serve until stdin closes. Logs go to
// stderr only — stdout is reserved for the MCP transport.
func defaultRunMCPServe(ctx context.Context, req mcpServeRequest) (int, error) {
	if _, err := os.Stat(req.AgentDir); err != nil {
		if os.IsNotExist(err) {
			return ExitInvalidConfig, fmt.Errorf("agent %q not found at %s", req.AgentName, req.AgentDir)
		}
		return ExitInvalidConfig, fmt.Errorf("stat %s: %w", req.AgentDir, err)
	}
	a, err := agent.Load(req.AgentDir)
	if err != nil {
		return ExitInvalidConfig, fmt.Errorf("load agent: %w", err)
	}
	if a.Name != req.AgentName {
		return ExitInvalidConfig, fmt.Errorf("agent name mismatch: agent.yaml=%q --agent=%q", a.Name, req.AgentName)
	}

	disp := tools.NewDispatcher()

	// Build the tool-call logger only when we have both a trace id (set by
	// the parent CLIBackend) and a logs directory. When either is absent
	// (standalone invocation, tests) we fall through with a nil emitter
	// and dispatcherHandler silently skips logging — preserving the
	// previous behavior for callers that don't need the recordings.
	var emitter toolCallEmitter
	var traceID string
	if req.LogsDir != "" {
		if id := os.Getenv(backend.TraceEnvVar); id != "" {
			store := yardlogs.NewStore(req.LogsDir)
			defer store.Close()
			logger := yardlogs.New(yardlogs.SourceCrew, yardlogs.Options{
				Store:   store,
				Version: app.Version,
			})
			emitter = cwlogs.NewRunnerAdapter(logger)
			traceID = id
		}
	}

	handler := &dispatcherHandler{
		agent:   a,
		disp:    disp,
		emitter: emitter,
		traceID: traceID,
	}

	srv := mcpserver.NewServer(handler, "shipyard-crew", app.Version)
	if err := srv.Serve(ctx, req.Stdin, req.Stdout); err != nil {
		// Context cancellation during shutdown is a clean exit.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ExitOK, nil
		}
		return ExitOnDemandInternal, fmt.Errorf("mcp serve: %w", err)
	}
	return ExitOK, nil
}

// dispatcherHandler bridges the mcpserver.Handler surface to the real
// tools.Dispatcher, which needs the *crew.Agent that owns the tools.
// emitter and traceID are set only when the parent process forwarded a
// trace id via SHIPYARD_CREW_TRACE_ID — otherwise both are nil/empty and
// Call falls back to the silent path that has always existed here.
type dispatcherHandler struct {
	agent   *crew.Agent
	disp    *tools.Dispatcher
	emitter toolCallEmitter
	traceID string
}

func (h *dispatcherHandler) Tools() []crew.Tool { return h.agent.Tools }

func (h *dispatcherHandler) Call(ctx context.Context, name string, args map[string]any) (tools.Envelope, error) {
	if h.emitter != nil {
		h.emitter.ToolCallStart(ctx, h.agent, h.traceID, name, args)
	}
	env, err := h.disp.Call(ctx, h.agent, name, args)
	if h.emitter != nil {
		h.emitter.ToolCallEnd(ctx, h.agent, h.traceID, name, env, err)
	}
	return env, err
}
