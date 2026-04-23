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
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/mcpserver"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

// Exit codes for the `mcp-serve` subcommand. Share the general addon table
// where the semantics match (2 invalid input, 20 failed to load agent) and
// add ExitOnDemandInternal for transport-level failures inside Serve.

type mcpServeRequest struct {
	AgentName string
	AgentDir  string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
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
	handler := &dispatcherHandler{agent: a, disp: disp}

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
type dispatcherHandler struct {
	agent *crew.Agent
	disp  *tools.Dispatcher
}

func (h *dispatcherHandler) Tools() []crew.Tool { return h.agent.Tools }

func (h *dispatcherHandler) Call(ctx context.Context, name string, args map[string]any) (tools.Envelope, error) {
	return h.disp.Call(ctx, h.agent, name, args)
}
