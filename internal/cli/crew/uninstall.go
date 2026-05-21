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

// unitFileForFn resolves the OS-level unit/plist path that a per-agent
// service would occupy, so detectActiveServices can decide whether the
// agent has a live registration. Tests substitute this with an in-memory
// resolver pointed at the test home.
var unitFileForFn = func(agentName string) (string, bool) {
	manager, err := crewctl.NewManager()
	if err != nil {
		return "", false
	}
	paths, err := manager.PathsFor(agentName)
	if err != nil {
		return "", false
	}
	return paths.UnitFile, true
}

// NewUninstallCmd returns the `shipyard crew uninstall` subcommand.
func NewUninstallCmd() *cobra.Command {
	return newUninstallCmdWith(nil)
}

func newUninstallCmdWith(inst *crewctl.Installer) *cobra.Command {
	var yes bool
	var forceServices bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the shipyard-crew AI agent runtime binary",
		Long: `Removes the shipyard-crew binary from ~/.local/bin/. Agent definitions under
~/.shipyard/crew/ are preserved so you can reinstall and resume where you left
off. To deregister individual agents before uninstalling, run "shipyard crew
fire <name>" for each one. Use --yes to skip the confirmation prompt.

If any agent still has an active OS-level service unit (launchd plist or
systemd .service), the uninstall aborts under --yes unless you also pass
--force-services. Without --force-services, the safer path is:

    shipyard crew fire <name>   # for each agent listed
    shipyard crew uninstall --yes`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target := inst
			if target == nil {
				var err error
				target, err = crewInstallerBuilder("")
				if err != nil {
					return err
				}
			}

			activeServices := detectActiveServices(target.StateDir, unitFileForFn)

			w := cmd.OutOrStdout()
			if len(activeServices) > 0 {
				if yes && !forceServices {
					return fmt.Errorf(
						"agents with active services: %s — run 'shipyard crew fire <name>' for each first, or pass --force-services to skip this check",
						strings.Join(activeServices, ", "),
					)
				}
				ui.Printf(w, "%s\n", ui.Paint(
					fmt.Sprintf("warning: %d agent(s) still have an active service unit: %s",
						len(activeServices), strings.Join(activeServices, ", ")),
					ui.StyleRed, ui.StyleBold,
				))
				ui.Printf(w, "%s\n", ui.Muted(
					"these services will be orphaned by uninstall — run 'shipyard crew fire <name>' first to clean them up.",
				))
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
	cmd.Flags().BoolVar(&forceServices, "force-services", false, "Proceed with --yes even if agents have active service units (services will be orphaned)")
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
	names := listAgents(stateDir)
	if len(names) == 0 {
		return
	}
	ui.Printf(w, "%s\n", ui.Muted(
		fmt.Sprintf("note: %d agent(s) still registered (%s) — run 'shipyard crew fire <name>' first to deregister per-agent services.",
			len(names), strings.Join(names, ", ")),
	))
}

// listAgents returns the subdirectory names of stateDir that contain an
// agent.yaml file. Empty/missing/unreadable stateDir yields an empty slice.
func listAgents(stateDir string) []string {
	if stateDir == "" {
		return nil
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil
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
	return names
}

// detectActiveServices returns the names of agents in stateDir that still
// have an OS-level service unit on disk (launchd plist or systemd .service).
// The unitFileFor callback is the resolver injection seam used in tests;
// when it returns ok=false for a given agent the entry is skipped.
func detectActiveServices(stateDir string, unitFileFor func(string) (string, bool)) []string {
	if unitFileFor == nil {
		return nil
	}
	var active []string
	for _, name := range listAgents(stateDir) {
		path, ok := unitFileFor(name)
		if !ok || path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			active = append(active, name)
		}
	}
	return active
}
