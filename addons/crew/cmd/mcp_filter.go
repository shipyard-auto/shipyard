package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/mcpfilter"
)

// mcpFilterRequest is the typed input consumed by defaultRunMCPFilter. It
// mirrors the flags accepted by `shipyard-crew mcp-filter` so the handler
// can be exercised in tests without parsing argv.
type mcpFilterRequest struct {
	UpstreamConfigPath string
	Allowed            []string
	Stdin              io.Reader
	Stdout             io.Writer
	Stderr             io.Writer
}

// runMCPFilterMode parses the `mcp-filter` subcommand flags and dispatches
// to the injectable handler. The subcommand acts as a stdio MCP proxy: claude
// (the downstream client) speaks to us, we relay frames to the upstream MCP
// server defined by --upstream-config, applying the tool whitelist passed
// via --tools.
//
// Wired into the CLIBackend's --mcp-config so claude never spawns the
// upstream MCP directly — every call funnels through this proxy when the
// agent declares an explicit `tools` list under `mcp_servers[]`.
func runMCPFilterMode(parent context.Context, deps runtimeDeps, args []string) int {
	fs := flag.NewFlagSet("shipyard-crew mcp-filter", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		upstreamConfig string
		toolsCSV       string
	)
	fs.StringVar(&upstreamConfig, "upstream-config", "", "path to JSON file describing the upstream MCP server to proxy (required)")
	fs.StringVar(&toolsCSV, "tools", "", "comma-separated whitelist of tool names; use \"*\" alone to disable filtering (required)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(deps.Stdout)
			fs.Usage()
			return ExitOK
		}
		fmt.Fprintf(deps.Stderr, "shipyard-crew mcp-filter: %s\n", err)
		return ExitInvalidInput
	}

	if upstreamConfig == "" {
		fmt.Fprintln(deps.Stderr, "shipyard-crew mcp-filter: --upstream-config is required")
		return ExitInvalidInput
	}
	if strings.TrimSpace(toolsCSV) == "" {
		fmt.Fprintln(deps.Stderr, "shipyard-crew mcp-filter: --tools is required (use --tools='*' to disable filtering)")
		return ExitInvalidInput
	}

	allowed := splitTools(toolsCSV)
	if len(allowed) == 0 {
		fmt.Fprintln(deps.Stderr, "shipyard-crew mcp-filter: --tools produced an empty whitelist")
		return ExitInvalidInput
	}

	req := mcpFilterRequest{
		UpstreamConfigPath: upstreamConfig,
		Allowed:            allowed,
		Stdin:              deps.Stdin,
		Stdout:             deps.Stdout,
		Stderr:             deps.Stderr,
	}

	sigCtx, cancel := deps.SignalCtx(parent)
	defer cancel()

	code, err := deps.RunMCPFilter(sigCtx, req)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard-crew mcp-filter: %s\n", err)
	}
	return code
}

// defaultRunMCPFilter is the production mcp-filter handler. It reads the
// upstream config JSON, builds the proxy and runs it until either side
// closes. All logging goes to stderr; stdout is reserved for the MCP
// transport.
func defaultRunMCPFilter(ctx context.Context, req mcpFilterRequest) (int, error) {
	raw, err := os.ReadFile(req.UpstreamConfigPath)
	if err != nil {
		return ExitInvalidConfig, fmt.Errorf("read upstream config %s: %w", req.UpstreamConfigPath, err)
	}
	var up mcpfilter.UpstreamConfig
	if err := json.Unmarshal(raw, &up); err != nil {
		return ExitInvalidConfig, fmt.Errorf("parse upstream config %s: %w", req.UpstreamConfigPath, err)
	}
	if err := up.Validate(); err != nil {
		return ExitInvalidConfig, fmt.Errorf("upstream config %s: %w", req.UpstreamConfigPath, err)
	}

	proxy := mcpfilter.Proxy{
		Allowed:  req.Allowed,
		Upstream: up,
		In:       req.Stdin,
		Out:      req.Stdout,
		Err:      req.Stderr,
	}
	if err := proxy.Run(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ExitOK, nil
		}
		return ExitOnDemandInternal, err
	}
	return ExitOK, nil
}

// splitTools turns "a,b,c" into ["a","b","c"], trimming whitespace and
// dropping empty entries. The validator on MCPServerRef already rejected
// empties and duplicates at agent.yaml load time; this is defense in depth
// against operators who hand-craft the wrapper invocation.
func splitTools(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}
