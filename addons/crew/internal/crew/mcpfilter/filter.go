// Package mcpfilter implements a stdio-to-stdio MCP proxy that enforces a
// per-agent tool whitelist on top of an upstream MCP server.
//
// Architecture:
//
//	claude  <-- stdio -->  mcpfilter.Proxy  <-- stdio -->  upstream MCP server
//
// The proxy passes initialize / notifications / resources / prompts through
// verbatim. Only two methods are intercepted:
//
//   - tools/list  → forwarded upstream; the response is filtered down to
//     names allowed by the whitelist before being returned to claude.
//   - tools/call  → checked against the whitelist locally; allowed calls are
//     forwarded; blocked calls are answered with a JSON-RPC error without
//     ever reaching upstream.
//
// The wildcard "*" disables filtering entirely (the proxy becomes a
// transparent pipe). Callers that detect "*" in agent.yaml should not invoke
// the filter at all and let claude talk to the upstream directly — there is
// no value in adding a hop. The filter still handles "*" as a no-op for
// safety / testability.
package mcpfilter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/mcpserver"
)

// UpstreamConfig describes how to spawn the real MCP server the proxy sits
// in front of. It mirrors the subset of fields Claude Code consumes from a
// stdio MCP entry in --mcp-config.
type UpstreamConfig struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Validate enforces the minimum invariants the proxy needs to spawn the
// upstream. Anything else (e.g. unknown fields) is ignored — the JSON file
// is written by buildMCPConfig, not by humans, so we trust the producer.
func (u UpstreamConfig) Validate() error {
	if u.Type != "" && u.Type != "stdio" {
		return fmt.Errorf("unsupported upstream type %q (only stdio)", u.Type)
	}
	if u.Command == "" {
		return errors.New("upstream command is empty")
	}
	return nil
}

// Proxy bridges a downstream client (typically claude) with an upstream MCP
// server, filtering by Allowed.
type Proxy struct {
	// Allowed is the whitelist of tool names. An empty slice blocks every
	// tool. The sentinel ["*"] disables filtering — every tool passes.
	Allowed []string

	// Upstream identifies the subprocess to spawn.
	Upstream UpstreamConfig

	// In, Out, Err wire the downstream client to the proxy. Typically
	// os.Stdin, os.Stdout, os.Stderr — but kept as fields for tests.
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Run spawns the upstream subprocess and shuttles JSON-RPC frames between
// it and the downstream client until either side closes. The function
// returns when the upstream process exits or the downstream closes its
// input stream.
//
// The provided ctx is used to cancel the upstream subprocess. Returning a
// non-nil error means the proxy exited abnormally; clean EOF from either
// side is a nil return.
func (p *Proxy) Run(ctx context.Context) error {
	if err := p.Upstream.Validate(); err != nil {
		return fmt.Errorf("mcpfilter: upstream: %w", err)
	}

	allowed, allowAll := buildAllowSet(p.Allowed)

	upCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(upCtx, p.Upstream.Command, p.Upstream.Args...)
	cmd.Env = mergeEnv(p.Upstream.Env)
	cmd.Stderr = p.Err
	upIn, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcpfilter: upstream stdin: %w", err)
	}
	upOut, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcpfilter: upstream stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcpfilter: upstream start: %w", err)
	}

	// pending tracks request IDs originally seen with method=tools/list so
	// we know to filter the corresponding response coming back from
	// upstream. Notification frames (id absent) don't get an entry.
	pending := &pendingMap{}

	downReader := mcpserver.NewReader(p.In)
	downWriter := mcpserver.NewWriter(p.Out)
	upReader := mcpserver.NewReader(upOut)
	upWriter := mcpserver.NewWriter(upIn)

	errCh := make(chan error, 2)

	// downstream → upstream: parse, filter tools/call, otherwise forward.
	go func() {
		errCh <- pumpDownstream(downReader, upWriter, downWriter, pending, allowed, allowAll)
	}()

	// upstream → downstream: parse, filter tools/list responses, otherwise
	// forward.
	go func() {
		errCh <- pumpUpstream(upReader, downWriter, pending, allowed, allowAll)
	}()

	// First side to return ends the proxy. Cancel upstream context so the
	// subprocess is reaped; drain the second goroutine.
	firstErr := <-errCh
	cancel()
	_ = upIn.Close()
	<-errCh
	_ = cmd.Wait()
	if firstErr != nil && !errors.Is(firstErr, io.EOF) {
		return firstErr
	}
	return nil
}

// buildAllowSet converts the Allowed slice into a lookup set, returning
// allowAll=true when the wildcard sentinel is present (in which case the
// returned set is nil — callers should branch on allowAll).
func buildAllowSet(allowed []string) (map[string]struct{}, bool) {
	for _, a := range allowed {
		if a == "*" {
			return nil, true
		}
	}
	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		set[a] = struct{}{}
	}
	return set, false
}

// pendingMap correlates JSON-RPC request IDs with the originating method so
// the upstream-side pump knows which responses to filter. IDs are stored as
// their raw JSON bytes (numbers and strings both accepted by the spec) so
// the comparison is exact.
type pendingMap struct {
	mu   sync.Mutex
	rows map[string]string // key: string(rawID), val: method
}

func (p *pendingMap) put(id json.RawMessage, method string) {
	if len(id) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rows == nil {
		p.rows = map[string]string{}
	}
	p.rows[string(id)] = method
}

func (p *pendingMap) takeMethod(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.rows[string(id)]
	delete(p.rows, string(id))
	return m
}

// mergeEnv reproduces os.Environ() plus the upstream-specific extras. We
// prefer pass-through of the host env (HOME, PATH, language locale, etc.)
// because MCP servers like chrome-devtools shell out to npx and need a
// realistic shell environment to find their binaries.
func mergeEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return nil // exec.Cmd interprets nil as "inherit parent env"
	}
	base := append([]string{}, os.Environ()...)
	for k, v := range extra {
		base = append(base, k+"="+v)
	}
	return base
}
