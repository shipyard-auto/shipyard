package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/addon"
	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/fairwaywiz"
	tuitype "github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

type fairwayConfigDeps struct {
	version       string
	socketPath    string
	stdinFD       func() uintptr
	stdoutFD      func() uintptr
	isInteractive func(uintptr) bool
	dial          func(context.Context, fairwayctl.Opts) (*fairwayctl.Client, error)
	newProgram    func(tea.Model, ...tea.ProgramOption) programRunner
}

type programRunner interface {
	Run() (tea.Model, error)
}

func newFairwayConfigCmd() *cobra.Command {
	return newFairwayConfigCmdWith(fairwayConfigDeps{})
}

func newFairwayConfigCmdWith(deps fairwayConfigDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Interactive fairway route configuration wizard",
		Long: strings.Join([]string{
			"Open the interactive fairway configuration wizard.",
			"",
			"Use this TUI to create, edit and remove webhook routes over the daemon socket.",
			"For scripting and CI, keep using `shipyard fairway route ...`.",
		}, "\n"),
		PreRunE: addon.RequirePreRun(addon.KindFairway),
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps = deps.withDefaults()
			if !deps.isInteractive(deps.stdinFD()) || !deps.isInteractive(deps.stdoutFD()) {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "shipyard fairway config requires a terminal (TTY); use 'shipyard fairway route add' for non-interactive setup")
				return tuitype.ErrNonInteractive
			}

			client, err := deps.dial(cmd.Context(), fairwayctl.Opts{
				SocketPath: deps.socketPath,
				Version:    deps.version,
			})
			if err != nil {
				return err
			}
			defer client.Close() //nolint:errcheck

			program := deps.newProgram(fairwaywiz.NewRoot(client), tea.WithAltScreen())
			model, err := program.Run()
			if err != nil {
				return err
			}
			if finished, ok := model.(*fairwaywiz.Root); ok && strings.TrimSpace(finished.Summary()) != "" {
				ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis(finished.Summary()))
			}
			return nil
		},
	}
}

func (d fairwayConfigDeps) withDefaults() fairwayConfigDeps {
	if d.version == "" {
		d.version = app.Version
	}
	if d.socketPath == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			d.socketPath = filepath.Join(homeDir, ".shipyard", "run", "fairway.sock")
		}
	}
	if d.stdinFD == nil {
		d.stdinFD = tuitype.StdinFD
	}
	if d.stdoutFD == nil {
		d.stdoutFD = func() uintptr { return os.Stdout.Fd() }
	}
	if d.isInteractive == nil {
		d.isInteractive = tuitype.IsInteractive
	}
	if d.dial == nil {
		d.dial = fairwayctl.Dial
	}
	if d.newProgram == nil {
		d.newProgram = func(model tea.Model, opts ...tea.ProgramOption) programRunner {
			return tea.NewProgram(model, opts...)
		}
	}
	return d
}
