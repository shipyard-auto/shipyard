package crew

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

// Exit codes produced by `shipyard crew apply`. The mapping mirrors the
// `shipyard-crew reconcile` subprocess contract documented in task 28a: the
// core CLI is a thin proxy for the addon, so the addon's exit codes are
// propagated as-is.
const (
	applyExitOK       = 0
	applyExitNotFound = 1
	applyExitError    = 2
)

// applyFlags captures the flags parsed by the apply command.
type applyFlags struct {
	DryRun bool
	JSON   bool
}

// applyDeps is the dependency injection struct used to make apply testable.
// All fields are optional — missing ones are filled by withDefaults.
type applyDeps struct {
	Home        string
	Stdout      io.Writer
	Stderr      io.Writer
	LookPath    func(string) (string, error)
	MakeCommand func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func (d applyDeps) withDefaults() applyDeps {
	if d.Home == "" {
		if h, err := shipyardHome(); err == nil {
			d.Home = h
		}
	}
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.MakeCommand == nil {
		d.MakeCommand = exec.CommandContext
	}
	return d
}

func newApplyCmd() *cobra.Command {
	return newApplyCmdWith(applyDeps{})
}

func newApplyCmdWith(deps applyDeps) *cobra.Command {
	var f applyFlags
	cmd := &cobra.Command{
		Use:   "apply <name>",
		Short: "Sync an AI agent's schedules and webhook routes",
		Long: `Reads the agent's agent.yaml and registers its scheduled runs as Shipyard
cron jobs and its webhook endpoints as fairway routes. Run this after
editing an agent definition or after "shipyard crew hire". Use --dry-run
to preview changes without applying them.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE:       requireInstalled,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := runApply(cmd.Context(), deps, args[0], f)
			if code == applyExitOK {
				return nil
			}
			return &ExitError{Code: code}
		},
	}
	cmd.Flags().BoolVar(&f.DryRun, "dry-run", false, "compute diff without applying")
	cmd.Flags().BoolVar(&f.JSON, "json", false, "emit JSON envelope instead of human output")
	return cmd
}

// runApply validates args, locates the addon binary, and spawns
// `shipyard-crew reconcile` with the matching flags. Exit codes from the
// subprocess are propagated verbatim.
func runApply(ctx context.Context, deps applyDeps, name string, f applyFlags) int {
	deps = deps.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}

	if !hireNameRe.MatchString(name) {
		fmt.Fprintf(deps.Stderr, "shipyard crew apply: invalid name %q\n", name)
		return applyExitNotFound
	}

	agentDir := filepath.Join(deps.Home, "crew", name)
	info, err := os.Stat(agentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(deps.Stderr, "shipyard crew apply: crew member not found: %s\n", name)
			return applyExitNotFound
		}
		fmt.Fprintf(deps.Stderr, "shipyard crew apply: stat %s: %s\n", agentDir, err)
		return applyExitNotFound
	}
	if !info.IsDir() {
		fmt.Fprintf(deps.Stderr, "shipyard crew apply: %s is not a directory\n", agentDir)
		return applyExitNotFound
	}

	bin, err := deps.LookPath(subprocessBinary)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard crew apply: %s not found in PATH; run 'shipyard crew install'\n", subprocessBinary)
		return applyExitError
	}

	args := []string{"reconcile", "--agent", name}
	if f.DryRun {
		args = append(args, "--dry-run")
	}
	if f.JSON {
		args = append(args, "--json")
	}

	cmd := deps.MakeCommand(ctx, bin, args...)
	cmd.Stdout = deps.Stdout
	cmd.Stderr = deps.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(deps.Stderr, "shipyard crew apply: subprocess failed: %s\n", err)
		return applyExitError
	}
	return applyExitOK
}
