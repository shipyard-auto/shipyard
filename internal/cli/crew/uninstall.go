package crew

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/addon"
	"github.com/shipyard-auto/shipyard/internal/crewctl"
	"github.com/shipyard-auto/shipyard/internal/ui"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

// ttyIsInteractive indirects the stdin-tty check so tests can stub it. The
// production value forwards to the tty package.
var ttyIsInteractive = func() bool { return tty.IsInteractive(tty.StdinFD()) }

// NewUninstallCmd returns the `shipyard crew uninstall` subcommand.
func NewUninstallCmd() *cobra.Command {
	return newUninstallCmdWith(nil)
}

func newUninstallCmdWith(inst *crewctl.Installer) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the shipyard-crew binary (preserves ~/.shipyard/crew/)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			target := inst
			if target == nil {
				var err error
				target, err = crewInstallerBuilder("")
				if err != nil {
					return err
				}
			}

			if !yes {
				if !ttyIsInteractive() {
					return fmt.Errorf("uninstall requires --yes in non-interactive mode")
				}
				ok, err := confirmUninstall(cmd.InOrStdin(), cmd.OutOrStdout())
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
			}

			w := cmd.OutOrStdout()
			ui.Printf(w, "%s\n", ui.SectionTitle("SHIPYARD CREW"))
			warnIfAgentsRegistered(w, target.StateDir)
			if err := target.Uninstall(cmd.Context()); err != nil {
				return err
			}
			_ = addon.NewRegistry("").Forget(addon.KindCrew)
			ui.Printf(w, "%s\n", ui.Emphasis("uninstalled: "+target.BinPath()))
			ui.Printf(w, "%s\n", ui.Muted("crew agents config preserved in "+target.StateDir))
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	return cmd
}

// confirmUninstall prompts the user and returns true for y/yes.
func confirmUninstall(in io.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, "remove shipyard-crew binary? your crew agents config in ~/.shipyard/crew/ will be preserved [y/N]: ")
	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	ans := strings.ToLower(strings.TrimSpace(sc.Text()))
	return ans == "y" || ans == "yes", nil
}

// warnIfAgentsRegistered prints a hint when stateDir contains agent folders,
// suggesting `shipyard crew fire <name>` before uninstalling so per-agent
// services are deregistered. It never fails the uninstall.
func warnIfAgentsRegistered(w io.Writer, stateDir string) {
	if stateDir == "" {
		return
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(stateDir, e.Name(), "agent.yaml")); err == nil {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return
	}
	ui.Printf(w, "%s\n", ui.Muted(
		fmt.Sprintf("note: %d agent(s) still registered (%s) — run 'shipyard crew fire <name>' first to deregister per-agent services.",
			len(names), strings.Join(names, ", ")),
	))
}
