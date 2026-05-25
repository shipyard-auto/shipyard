package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/commsctl"
	"github.com/shipyard-auto/shipyard/internal/ui"
)

// commsStatusDeps captures every IO/filesystem dependency for `shipyard
// comms status` so tests can fully isolate. All fields are optional; the
// status command calls withDefaults to fill them in for production.
type commsStatusDeps struct {
	binPath          string
	stateDir         string
	isInstalled      func() bool
	installedVersion func() (string, error)
}

type commsStatusBinary struct {
	Path       string `json:"path,omitempty"`
	Version    string `json:"version,omitempty"`
	Installed  bool   `json:"installed"`
	Functional bool   `json:"functional"`
	Error      string `json:"error,omitempty"`
}

type commsStatusReport struct {
	State    string            `json:"state"`
	Binary   commsStatusBinary `json:"binary"`
	StateDir string            `json:"stateDir"`
}

func newCommsStatusCmd() *cobra.Command {
	return newCommsStatusCmdWith(commsStatusDeps{})
}

func newCommsStatusCmdWith(deps commsStatusDeps) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show shipyard-comms installation status",
		Long: `Reports whether the shipyard-comms binary is installed under ~/.local/bin/,
the version it reports via --version, and the path to the state directory.

Comms is stateless in v1, so there is no daemon or service to query —
this command is purely about the binary on disk and the local state path.
Use --json for machine-readable output.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			report := collectCommsStatus(deps)
			if jsonOutput {
				return renderCommsStatusJSON(cmd.OutOrStdout(), report)
			}
			renderCommsStatusHuman(cmd.OutOrStdout(), report)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print status as JSON")
	return cmd
}

func collectCommsStatus(deps commsStatusDeps) commsStatusReport {
	deps = deps.withDefaults()

	report := commsStatusReport{
		State:    "not installed",
		Binary:   commsStatusBinary{Path: deps.binPath},
		StateDir: deps.stateDir,
	}

	report.Binary.Installed = deps.isInstalled()
	if !report.Binary.Installed {
		return report
	}

	installedVersion, err := deps.installedVersion()
	if err != nil {
		report.State = "binary not functional"
		report.Binary.Error = err.Error()
		return report
	}
	report.Binary.Functional = true
	report.Binary.Version = installedVersion
	report.State = "installed"
	return report
}

func (d commsStatusDeps) withDefaults() commsStatusDeps {
	if d.binPath == "" || d.stateDir == "" || d.isInstalled == nil || d.installedVersion == nil {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			if d.binPath == "" {
				d.binPath = filepath.Join(homeDir, ".local", "bin", commsctl.BinaryName)
			}
			if d.stateDir == "" {
				d.stateDir = filepath.Join(homeDir, ".shipyard", "comms")
			}
			if d.isInstalled == nil || d.installedVersion == nil {
				inst := &commsctl.Installer{
					BinDir: filepath.Join(homeDir, ".local", "bin"),
				}
				if d.isInstalled == nil {
					d.isInstalled = inst.IsInstalled
				}
				if d.installedVersion == nil {
					d.installedVersion = inst.InstalledVersion
				}
			}
		}
	}
	if d.isInstalled == nil {
		d.isInstalled = func() bool { return false }
	}
	if d.installedVersion == nil {
		d.installedVersion = func() (string, error) {
			return "", fmt.Errorf("comms: home dir unavailable")
		}
	}
	return d
}

func renderCommsStatusHuman(w io.Writer, report commsStatusReport) {
	ui.Printf(w, "%s\n", ui.SectionTitle("Comms"))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  State:\t%s\n", paintCommsState(report.State))
	if report.Binary.Version != "" {
		fmt.Fprintf(tw, "  Version:\t%s\n", report.Binary.Version)
	}
	if report.Binary.Path != "" {
		fmt.Fprintf(tw, "  Binary:\t%s\n", report.Binary.Path)
	}
	if report.StateDir != "" {
		fmt.Fprintf(tw, "  State dir:\t%s\n", report.StateDir)
	}
	_ = tw.Flush()

	if report.Binary.Installed && !report.Binary.Functional && report.Binary.Error != "" {
		ui.Printf(w, "\n%s\n", ui.Paint(
			fmt.Sprintf("Binary at %s is present but does not respond to --version.", report.Binary.Path),
			ui.StyleRed, ui.StyleBold,
		))
		ui.Printf(w, "  %s\n", ui.Muted("error: "+report.Binary.Error))
	}
}

func renderCommsStatusJSON(w io.Writer, report commsStatusReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func paintCommsState(state string) string {
	switch state {
	case "installed":
		return ui.Paint(state, ui.StyleBold, ui.StyleCyan)
	case "binary not functional":
		return ui.Paint(state, ui.StyleBold, ui.StyleRed)
	default:
		return state
	}
}
