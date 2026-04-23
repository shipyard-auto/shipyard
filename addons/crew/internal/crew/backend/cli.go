package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/conversation"
)

const (
	cliMaxStdoutBytes = 4 * 1024 * 1024
	cliMaxStderrBytes = 4 * 1024 * 1024
	// cliWaitDelay bounds how long Wait blocks on pipe drainage after the
	// process is signaled. Without it, grandchildren (e.g. `sleep` under
	// `sh -c`) can hold stdout/stderr FDs open well past cancellation.
	cliWaitDelay = 500 * time.Millisecond
	// defaultSystemPromptFlag is the argv flag used by the Claude Code CLI
	// to inject a system prompt. Agents that target other CLIs override this
	// via Backend.SystemPromptFlag.
	defaultSystemPromptFlag = "--append-system-prompt"

	// internalServerKey is the name under which we register ourselves in
	// the synthetic --mcp-config file. Claude Code uses this key as the
	// server identifier shown in traces.
	internalServerKey = "shipyard-crew-internal"
)

// defaultSessionRegex matches "session=<id>" or "session: <id>" emitted by
// external CLIs on stderr. It is intentionally permissive (allows dashes and
// mixed case) so common Claude-Code-ish session payloads are captured
// without per-CLI configuration.
var defaultSessionRegex = regexp.MustCompile(`session[:=]\s*([a-zA-Z0-9-]+)`)

// CLIBackend implements Backend by spawning the external CLI declared in
// Agent.Backend.Command, injecting the agent's system prompt via a flag,
// piping the user message into stdin, and reading stdout as the turn
// result.
//
// Tool orchestration for inline agent tools is delegated to the CLI
// process through an MCP stdio bridge: when the agent declares any tool
// or any mcp_servers entry, the backend writes a temporary --mcp-config
// JSON file that (a) registers `shipyard-crew mcp-serve --agent <name>`
// as an internal server and (b) copies over any externally referenced
// servers from ~/.claude.json. The CLI then spawns both and exposes their
// tools to the LLM during the turn. The ToolDispatcher argument is still
// ignored — the dispatcher runs inside the internal MCP server subprocess,
// not here.
type CLIBackend struct {
	sessionRegex *regexp.Regexp
	homeEnv      func(string) string
	userHomeDir  func() (string, error)
	executable   func() (string, error)
	selfPath     string // overrides executable() when non-empty; test hook
}

// NewCLIBackend returns a CLIBackend with the default session-id regex
// and default resolvers for the current-executable path, environment and
// home directory.
func NewCLIBackend() *CLIBackend {
	return &CLIBackend{
		sessionRegex: defaultSessionRegex,
		homeEnv:      os.Getenv,
		userHomeDir:  os.UserHomeDir,
		executable:   os.Executable,
	}
}

// WithSessionRegex overrides the default stderr session-id extractor. The
// regex must have at least one capture group; the first sub-match is taken
// as the session id. Returns the same backend for chaining.
func (b *CLIBackend) WithSessionRegex(r *regexp.Regexp) *CLIBackend {
	if r != nil {
		b.sessionRegex = r
	}
	return b
}

// WithSelfPath overrides the path used to invoke this binary from within
// the synthesised MCP config. Intended for tests that want to point at a
// fake mcp-server shim; production should rely on os.Executable.
func (b *CLIBackend) WithSelfPath(p string) *CLIBackend {
	b.selfPath = p
	return b
}

// WithHomeEnv overrides os.Getenv — used by tests to inject $HOME /
// $SHIPYARD_HOME without polluting the real process env.
func (b *CLIBackend) WithHomeEnv(fn func(string) string) *CLIBackend {
	if fn != nil {
		b.homeEnv = fn
	}
	return b
}

// WithUserHomeDir overrides os.UserHomeDir. Used by tests to pin
// ~/.claude.json lookup to a tempdir.
func (b *CLIBackend) WithUserHomeDir(fn func() (string, error)) *CLIBackend {
	if fn != nil {
		b.userHomeDir = fn
	}
	return b
}

var _ Backend = (*CLIBackend)(nil)

// Run executes the CLI subprocess, piping RunInput.User into stdin and
// honouring context cancellation.
func (b *CLIBackend) Run(ctx context.Context, in RunInput, _ ToolDispatcher) (RunOutput, error) {
	if in.Agent == nil || len(in.Agent.Backend.Command) == 0 {
		return RunOutput{}, errors.New("cli backend: empty command")
	}

	args := append([]string(nil), in.Agent.Backend.Command...)
	if prompt := strings.TrimSpace(in.Prompt); prompt != "" {
		flag := in.Agent.Backend.SystemPromptFlag
		if flag == "" {
			flag = defaultSystemPromptFlag
		}
		args = append(args, flag, in.Prompt)
	}
	if in.History.SessionID != "" {
		args = append(args, "--resume", in.History.SessionID)
	}

	cfgPath, cleanup, err := b.buildMCPConfig(in.Agent)
	if err != nil {
		return RunOutput{}, fmt.Errorf("cli mcp config: %w", err)
	}
	defer cleanup()

	if cfgPath != "" {
		// --permission-mode bypassPermissions is required because claude --print
		// has no TTY to approve tool calls interactively; without it the model
		// replies "preciso de permissão…" instead of invoking the tool.
		// --strict-mcp-config already confines the tool surface to the servers
		// we synthesised (the internal crew bridge + agent-declared refs).
		args = append(args,
			"--mcp-config", cfgPath,
			"--strict-mcp-config",
			"--permission-mode", "bypassPermissions",
		)
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.WaitDelay = cliWaitDelay
	cmd.Stdin = strings.NewReader(in.User)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, limit: cliMaxStdoutBytes}
	cmd.Stderr = &limitedWriter{w: &stderr, limit: cliMaxStderrBytes}

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RunOutput{}, fmt.Errorf("cli canceled: %w", ctxErr)
		}
		return RunOutput{}, fmt.Errorf("cli run: %w; stderr=%s", err, truncate(stderr.String(), 1024))
	}

	sid := b.extractSessionID(stderr.String())
	if sid == "" {
		sid = in.History.SessionID
	}

	return RunOutput{
		Text:    strings.TrimRight(stdout.String(), "\n"),
		History: conversation.History{SessionID: sid},
		Usage:   Usage{},
	}, nil
}

// buildMCPConfig materialises a temporary `--mcp-config` JSON file for the
// given agent. It returns ("", noop, nil) when the agent declares neither
// tools nor mcp_servers — preserving the original zero-config behaviour so
// existing agents do not pay for a feature they don't use.
//
// Otherwise it writes a file of the form:
//
//	{
//	  "mcpServers": {
//	    "shipyard-crew-internal": { "type":"stdio", "command":<self>,
//	                                "args":["mcp-serve","--agent",<name>] },
//	    "<ref>": <copied verbatim from ~/.claude.json>,
//	    ...
//	  }
//	}
//
// Cleanup is idempotent and always safe to defer, even on the error path.
func (b *CLIBackend) buildMCPConfig(agent *crew.Agent) (string, func(), error) {
	noop := func() {}
	if agent == nil {
		return "", noop, errors.New("nil agent")
	}
	if len(agent.Tools) == 0 && len(agent.MCPServers) == 0 {
		return "", noop, nil
	}

	servers := map[string]json.RawMessage{}

	if len(agent.Tools) > 0 {
		self, err := b.resolveSelfPath()
		if err != nil {
			return "", noop, fmt.Errorf("self path: %w", err)
		}
		internal := map[string]any{
			"type":    "stdio",
			"command": self,
			"args":    []string{"mcp-serve", "--agent", agent.Name},
		}
		raw, err := json.Marshal(internal)
		if err != nil {
			return "", noop, fmt.Errorf("marshal internal server: %w", err)
		}
		servers[internalServerKey] = raw
	}

	if len(agent.MCPServers) > 0 {
		src, err := LoadClaudeMCPs(b.userHomeDir)
		if err != nil {
			return "", noop, err
		}
		resolved, err := ResolveServerRefs(agent.MCPServers, src)
		if err != nil {
			return "", noop, err
		}
		for k, v := range resolved {
			if _, clash := servers[k]; clash {
				return "", noop, fmt.Errorf("mcp_servers: ref %q collides with internal server key", k)
			}
			servers[k] = v
		}
	}

	body := map[string]any{"mcpServers": servers}
	data, err := json.Marshal(body)
	if err != nil {
		return "", noop, fmt.Errorf("marshal mcp config: %w", err)
	}

	tmp, err := os.CreateTemp("", "shipyard-crew-mcp-*.json")
	if err != nil {
		return "", noop, fmt.Errorf("create temp: %w", err)
	}
	path := tmp.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", noop, fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("close temp: %w", err)
	}
	return path, cleanup, nil
}

// resolveSelfPath returns the path to the running shipyard-crew binary,
// honouring the WithSelfPath override if set. We resolve lazily because
// os.Executable can be surprisingly expensive on some systems and many
// agents do not need it at all.
func (b *CLIBackend) resolveSelfPath() (string, error) {
	if b.selfPath != "" {
		return b.selfPath, nil
	}
	if b.executable == nil {
		return os.Executable()
	}
	return b.executable()
}

func (b *CLIBackend) extractSessionID(stderr string) string {
	if b.sessionRegex == nil {
		return ""
	}
	m := b.sessionRegex.FindStringSubmatch(stderr)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// limitedWriter forwards writes to the underlying writer up to limit bytes
// and silently discards the rest. Callers get no short-write error — the
// intent is to cap resource use while letting the subprocess finish
// normally.
type limitedWriter struct {
	w     io.Writer
	limit int
	n     int
	cut   bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.cut {
		return len(p), nil
	}
	remain := l.limit - l.n
	if remain <= 0 {
		l.cut = true
		return len(p), nil
	}
	if len(p) > remain {
		nw, _ := l.w.Write(p[:remain])
		l.n += nw
		l.cut = true
		return len(p), nil
	}
	nw, err := l.w.Write(p)
	l.n += nw
	return nw, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
