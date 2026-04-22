package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"text/template"
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
)

// defaultSessionRegex matches "session=<id>" or "session: <id>" emitted by
// external CLIs on stderr. It is intentionally permissive (allows dashes and
// mixed case) so common Claude-Code-ish session payloads are captured
// without per-CLI configuration.
var defaultSessionRegex = regexp.MustCompile(`session[:=]\s*([a-zA-Z0-9-]+)`)

// promptPlaceholderRe detects any use of the {{.Prompt}} or {{.PromptFile}}
// placeholders in a Backend.Command argv element. It is deliberately lax on
// whitespace to match common Go-template writing conventions.
var promptPlaceholderRe = regexp.MustCompile(`\{\{\s*\.(Prompt|PromptFile)\s*\}\}`)

// CLIBackend implements Backend by spawning the external CLI declared in
// Agent.Backend.Command, piping the user message into stdin, and reading
// stdout as the turn result. Tool orchestration is delegated to the CLI —
// the provided ToolDispatcher is always ignored.
//
// The system prompt (contents of prompt.md) is surfaced to the external CLI
// via Go-template placeholders in the argv:
//
//   - {{.Prompt}}     → expanded to the literal prompt content.
//   - {{.PromptFile}} → expanded to a path to a tempfile containing the prompt.
//
// If the agent has a non-empty prompt but the argv references neither
// placeholder, Run fails fast instead of silently dropping the agent's
// identity.
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

// cliTemplateData is the data model exposed to Backend.Command argv
// templates. It is deliberately narrow so users cannot reach for agent
// internals by accident.
type cliTemplateData struct {
	Prompt     string
	PromptFile string
}

// Run executes the CLI subprocess, piping RunInput.User into stdin and
// honouring context cancellation. ToolDispatcher is ignored — CLI-style
// backends delegate tool orchestration to the external process.
func (b *CLIBackend) Run(ctx context.Context, in RunInput, _ ToolDispatcher) (RunOutput, error) {
	if in.Agent == nil || len(in.Agent.Backend.Command) == 0 {
		return RunOutput{}, errors.New("cli backend: empty command")
	}

	cmdTemplate := in.Agent.Backend.Command
	prompt := in.Prompt
	trimmedPrompt := strings.TrimSpace(prompt)

	uses := detectPromptPlaceholders(cmdTemplate)
	if trimmedPrompt != "" && !uses.any() {
		return RunOutput{}, errors.New(`cli backend: agent has a non-empty prompt.md but backend.command does not reference it; use {{.Prompt}} or {{.PromptFile}} — e.g. ["claude","--print","--append-system-prompt","{{.Prompt}}"]`)
	}

	data := cliTemplateData{Prompt: prompt}
	var promptFileCleanup func()
	if uses.file {
		path, cleanup, err := writePromptTempFile(prompt)
		if err != nil {
			return RunOutput{}, fmt.Errorf("cli backend: prompt tempfile: %w", err)
		}
		data.PromptFile = path
		promptFileCleanup = cleanup
	}
	defer func() {
		if promptFileCleanup != nil {
			promptFileCleanup()
		}
	}()

	args, err := expandArgs(cmdTemplate, data)
	if err != nil {
		return RunOutput{}, fmt.Errorf("cli backend: %w", err)
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

// promptUsage reports which prompt placeholders appear in the argv template.
type promptUsage struct {
	inline bool
	file   bool
}

func (u promptUsage) any() bool { return u.inline || u.file }

func detectPromptPlaceholders(argv []string) promptUsage {
	var u promptUsage
	for _, a := range argv {
		for _, m := range promptPlaceholderRe.FindAllStringSubmatch(a, -1) {
			switch m[1] {
			case "Prompt":
				u.inline = true
			case "PromptFile":
				u.file = true
			}
		}
	}
	return u
}

// expandArgs applies text/template substitution to each argv element. Only
// elements containing "{{" are parsed, to keep the common case cheap and to
// avoid surfacing template errors for literal argv.
func expandArgs(argv []string, data cliTemplateData) ([]string, error) {
	out := make([]string, 0, len(argv))
	for i, a := range argv {
		expanded, err := expandTemplate(a, data)
		if err != nil {
			return nil, fmt.Errorf("expand argv[%d]: %w", i, err)
		}
		out = append(out, expanded)
	}
	return out, nil
}

func expandTemplate(s string, data cliTemplateData) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	tmpl, err := template.New("argv").Option("missingkey=error").Parse(s)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// writePromptTempFile writes the prompt to a private tempfile and returns the
// path plus a cleanup function. The file is created with 0600 and lives only
// for the duration of a single Run.
func writePromptTempFile(prompt string) (string, func(), error) {
	f, err := os.CreateTemp("", "crew-prompt-*.md")
	if err != nil {
		return "", nil, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := f.WriteString(prompt); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
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
