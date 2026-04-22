package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

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
)

// defaultSessionRegex matches "session=<id>" or "session: <id>" emitted by
// external CLIs on stderr. It is intentionally permissive (allows dashes and
// mixed case) so common Claude-Code-ish session payloads are captured
// without per-CLI configuration.
var defaultSessionRegex = regexp.MustCompile(`session[:=]\s*([a-zA-Z0-9-]+)`)

// CLIBackend implements Backend by spawning the external CLI declared in
// Agent.Backend.Command, injecting the agent's system prompt via a flag,
// piping the user message into stdin, and reading stdout as the turn
// result. Tool orchestration is delegated to the CLI — the provided
// ToolDispatcher is always ignored.
//
// The system prompt (contents of prompt.md) is appended to the argv as
// `[SystemPromptFlag, prompt]` before invocation. The flag defaults to
// "--append-system-prompt" (Claude Code convention) and can be overridden
// per-agent via Agent.Backend.SystemPromptFlag.
type CLIBackend struct {
	sessionRegex *regexp.Regexp
}

// NewCLIBackend returns a CLIBackend with the default session-id regex.
func NewCLIBackend() *CLIBackend {
	return &CLIBackend{sessionRegex: defaultSessionRegex}
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

var _ Backend = (*CLIBackend)(nil)

// Run executes the CLI subprocess, piping RunInput.User into stdin and
// honouring context cancellation. ToolDispatcher is ignored — CLI-style
// backends delegate tool orchestration to the external process.
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
