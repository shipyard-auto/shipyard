package crew

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	versiondata "github.com/shipyard-auto/shipyard"
	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/crewctl"
)

// VersionNotInstalled is the human-readable placeholder used by the text
// output of `shipyard crew version` when no binary exists at BinPath.
const VersionNotInstalled = "(not installed)"

// VersionNotFunctional is the human-readable placeholder used when the
// binary file is present but `--version` could not be executed (wrong
// permissions, corrupt binary, timed out, etc.). The accompanying `error`
// field carries the underlying message.
const VersionNotFunctional = "(present but not functional)"

// crewVersionOutput is the JSON envelope emitted by `shipyard crew version
// --json`. Existing consumers continue to see `shipyard`/`shipyard_crew`/
// `installed`; the new `functional` and `error` fields are additive.
type crewVersionOutput struct {
	Shipyard     string `json:"shipyard"`
	ShipyardCrew string `json:"shipyard_crew"`
	Installed    bool   `json:"installed"`
	Functional   bool   `json:"functional"`
	Error        string `json:"error,omitempty"`
}

// NewVersionCmd returns the `shipyard crew version` subcommand.
func NewVersionCmd() *cobra.Command {
	return newVersionCmdWith(nil, "")
}

// newVersionCmdWith builds the version command. When inst is nil, a
// production installer is constructed to resolve InstalledVersion(); when
// coreVersion is empty, app.Version is used.
func newVersionCmdWith(inst *crewctl.Installer, coreVersion string) *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show shipyard and shipyard-crew versions",
		Long: `Prints the installed versions of the shipyard core binary and the
shipyard-crew AI agent runtime addon. Use --json to emit machine-readable
output suitable for scripts or bug reports.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			core := coreVersion
			if core == "" {
				core = app.Version
			}

			target := inst
			if target == nil {
				v := versiondata.ComponentVersion("crew")
				built, err := crewInstallerBuilder(v)
				if err != nil {
					return err
				}
				target = built
			}

			installed := target.IsInstalled()
			addonVersion, vErr := target.InstalledVersion()
			functional := vErr == nil

			var addon, errMsg string
			switch {
			case functional:
				addon = addonVersion
			case installed:
				addon = VersionNotFunctional
				errMsg = vErr.Error()
			default:
				addon = VersionNotInstalled
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return json.NewEncoder(w).Encode(crewVersionOutput{
					Shipyard:     core,
					ShipyardCrew: addon,
					Installed:    installed,
					Functional:   functional,
					Error:        errMsg,
				})
			}

			fmt.Fprintf(w, "shipyard      %s\n", core)
			fmt.Fprintf(w, "shipyard-crew %s\n", addon)
			if errMsg != "" {
				fmt.Fprintf(w, "  error: %s\n", errMsg)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON output")
	return cmd
}
