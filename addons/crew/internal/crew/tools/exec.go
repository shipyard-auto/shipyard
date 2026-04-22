package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/template"
)

// ExecTimeout is the hard cap for a single exec tool invocation on v1.
// Per-tool configuration is tracked in the roadmap (1.3).
const (
	ExecTimeout    = 60 * time.Second
	StdoutMaxBytes = 4 * 1024 * 1024
	StderrMaxBytes = 4 * 1024 * 1024
)

// ExecDriver runs tool.Command as a local subprocess. It is safe for
// concurrent use by independent goroutines.
type ExecDriver struct {
	Now     func() time.Time
	Timeout time.Duration
}

// NewExecDriver returns a driver with production defaults.
func NewExecDriver() *ExecDriver {
	return &ExecDriver{Now: time.Now, Timeout: ExecTimeout}
}

// Execute implements Driver. It renders the command template, invokes the
// subprocess with the JSON-encoded input on stdin, enforces the stdout
// budget and translates the result into an Envelope.
func (d *ExecDriver) Execute(ctx context.Context, tool crew.Tool, input map[string]any, dc DriverContext) (Envelope, error) {
	if tool.Protocol != crew.ToolExec {
		return Envelope{}, fmt.Errorf("exec driver: wrong protocol %q", tool.Protocol)
	}
	if len(tool.Command) == 0 {
		return Envelope{}, errors.New("exec driver: empty command")
	}

	tplCtx := template.Context{
		Input: input,
		Env:   dc.Env,
		Agent: map[string]string{
			"name": dc.AgentName,
			"dir":  dc.AgentDir,
		},
	}
	rendered, err := template.RenderSlice(tool.Command, tplCtx)
	if err != nil {
		return Failure("exec: render command failed: "+err.Error(), nil), nil
	}

	timeout := d.Timeout
	if timeout == 0 {
		timeout = ExecTimeout
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, rendered[0], rendered[1:]...)

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return Failure("exec: marshal input failed: "+err.Error(), nil), nil
	}
	cmd.Stdin = bytes.NewReader(inputJSON)

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutLW := &limitedWriter{W: &stdoutBuf, N: StdoutMaxBytes}
	stderrLW := &limitedWriter{W: &stderrBuf, N: StderrMaxBytes}
	cmd.Stdout = stdoutLW
	cmd.Stderr = stderrLW

	cmd.Env = buildEnv(dc.Env)

	runErr := cmd.Run()

	if ctx.Err() == context.Canceled {
		return Failure("exec: canceled", nil), nil
	}
	if execCtx.Err() == context.DeadlineExceeded {
		return Failure("exec: tool timeout", map[string]any{
			"timeout_seconds": timeout.Seconds(),
			"stdout_bytes":    stdoutBuf.Len(),
			"stderr":          truncateStr(stderrBuf.String(), 2048),
		}), nil
	}

	if stdoutLW.Overflowed {
		return Failure("exec: stdout exceeded "+byteCountStr(StdoutMaxBytes), map[string]any{
			"stderr": truncateStr(stderrBuf.String(), 2048),
		}), nil
	}

	if runErr != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		}
		return Failure(fmt.Sprintf("tool crashed: exit=%d", exitCode), map[string]any{
			"stderr": truncateStr(stderrBuf.String(), 2048),
		}), nil
	}

	env, parseErr := Parse(stdoutBuf.Bytes())
	if parseErr != nil {
		return Failure("exec: invalid envelope: "+parseErr.Error(), map[string]any{
			"stdout_sample": truncateStr(stdoutBuf.String(), 512),
			"stderr_sample": truncateStr(stderrBuf.String(), 512),
		}), nil
	}
	return env, nil
}

// limitedWriter wraps a writer and drops bytes past the configured budget.
// It intentionally reports len(p) back to the caller even when dropping, so
// the subprocess does not receive SIGPIPE and can continue draining until
// exit. Overflow is signalled via the Overflowed flag.
type limitedWriter struct {
	W          io.Writer
	N          int64
	Overflowed bool
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if lw.N <= 0 {
		lw.Overflowed = true
		return len(p), nil
	}
	if int64(len(p)) > lw.N {
		_, err := lw.W.Write(p[:lw.N])
		lw.N = 0
		lw.Overflowed = true
		return len(p), err
	}
	n, err := lw.W.Write(p)
	lw.N -= int64(n)
	return n, err
}

// buildEnv merges the process environment with the driver-injected map.
// Injected keys win on collision.
func buildEnv(inject map[string]string) []string {
	base := os.Environ()
	if len(inject) == 0 {
		return base
	}
	merged := make([]string, 0, len(base)+len(inject))
	seen := make(map[string]bool, len(inject))
	for k, v := range inject {
		merged = append(merged, k+"="+v)
		seen[k] = true
	}
	for _, e := range base {
		eq := strings.IndexByte(e, '=')
		if eq < 0 {
			continue
		}
		if seen[e[:eq]] {
			continue
		}
		merged = append(merged, e)
	}
	return merged
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}

func byteCountStr(n int64) string {
	return fmt.Sprintf("%d bytes", n)
}
