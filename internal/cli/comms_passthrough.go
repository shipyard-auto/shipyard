package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/commsctl"
)

// commsPassthroughDeps captures the IO and process boundaries of the
// pass-through commands so tests can swap them. ResolveBinary returns
// the absolute path to shipyard-comms (or an error); MakeCommand is
// exec.CommandContext in production.
type commsPassthroughDeps struct {
	ResolveBinary func() (string, error)
	MakeCommand   func(ctx context.Context, name string, args ...string) *exec.Cmd
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
}

func (d commsPassthroughDeps) withDefaults() commsPassthroughDeps {
	if d.ResolveBinary == nil {
		d.ResolveBinary = commsctl.ResolveBinary
	}
	if d.MakeCommand == nil {
		d.MakeCommand = exec.CommandContext
	}
	if d.Stdin == nil {
		d.Stdin = os.Stdin
	}
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	return d
}

// runCommsPassthrough resolves the shipyard-comms binary, exec.Command's
// it with the given args, wires stdin/stdout/stderr through and
// propagates the addon's exit code. Treats a missing binary as an
// actionable error pointing at `shipyard comms install`.
func runCommsPassthrough(ctx context.Context, deps commsPassthroughDeps, args []string) error {
	deps = deps.withDefaults()

	bin, err := deps.ResolveBinary()
	if err != nil {
		if errors.Is(err, commsctl.ErrNotInstalled) {
			return fmt.Errorf("shipyard-comms is not installed — run 'shipyard comms install' first")
		}
		return fmt.Errorf("resolve shipyard-comms: %w", err)
	}

	cmd := deps.MakeCommand(ctx, bin, args...)
	cmd.Stdin = deps.Stdin
	cmd.Stdout = deps.Stdout
	cmd.Stderr = deps.Stderr

	if err := cmd.Run(); err != nil {
		// Exit error: re-surface as a cobra error but DO NOT wrap a
		// secondary message — the addon binary already wrote a human
		// explanation to stderr.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return fmt.Errorf("invoke shipyard-comms: %w", err)
	}
	return nil
}

// newCommsChannelCmd returns the `shipyard comms channel <subcommand>`
// passthrough. DisableFlagParsing is critical: cobra would otherwise
// intercept --type / --name / --json before they reach the addon binary.
func newCommsChannelCmd() *cobra.Command {
	return newCommsChannelCmdWith(commsPassthroughDeps{})
}

func newCommsChannelCmdWith(deps commsPassthroughDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "channel",
		Short: "Manage comms channels (add, list)",
		Long: `Forwards to the shipyard-comms binary. Available subcommands:

  shipyard comms channel add  --type <t> --name <n> [--<type>-token <tok>] [--json]
  shipyard comms channel list [--json]

Run 'shipyard-comms channel' (with no shipyard prefix) for the full
addon-side help text.`,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// cobra hands us everything after "comms channel" as args.
			// Prepend "channel" so the addon binary sees the same
			// subcommand structure.
			passed := append([]string{"channel"}, args...)
			return runCommsPassthrough(cmd.Context(), deps, passed)
		},
	}
}

// newCommsSendCmd returns the `shipyard comms send` passthrough.
func newCommsSendCmd() *cobra.Command {
	return newCommsSendCmdWith(commsPassthroughDeps{})
}

func newCommsSendCmdWith(deps commsPassthroughDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "send",
		Short: "Send a message through a configured comms channel",
		Long: `Forwards to the shipyard-comms binary. Common usage:

  shipyard comms send --channel <name|id> --to <recipient> --text "hello"
  shipyard comms send --channel <name|id> --to <recipient> --stdin-body <<<"piped body"
  shipyard comms send --channel <name|id> --to <recipient> --text "hi" --json

Run 'shipyard-comms send' (with no shipyard prefix) for the full
addon-side help text.`,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			passed := append([]string{"send"}, args...)
			return runCommsPassthrough(cmd.Context(), deps, passed)
		},
	}
}
