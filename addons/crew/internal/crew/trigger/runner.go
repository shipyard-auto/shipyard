// Package trigger hosts the reconcilers that translate `agent.yaml` trigger
// declarations into side-effects on external shipyard subsystems (cron,
// fairway). Each reconciler is idempotent and only talks to the core CLI via
// subprocess: the crew addon never imports core packages.
package trigger

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// CommandRunner runs a subprocess and returns its combined stdout bytes.
// Implementations must return an error that includes the process exit code
// information so callers can branch on the outcome.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the default CommandRunner, backed by os/exec.
type ExecRunner struct{}

// Run executes name with the given arguments and returns stdout. Stderr is
// attached to the returned error when the process fails.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s %v: %w: %s", name, args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
