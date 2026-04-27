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
// output of `shipyard crew version` when the addon is missing.
const VersionNotInstalled = "(not installed)"

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

			addon, err := target.InstalledVersion()
			installed := err == nil
			if !installed {
				addon = VersionNotInstalled
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return json.NewEncoder(w).Encode(map[string]any{
					"shipyard":      core,
					"shipyard_crew": addon,
					"installed":     installed,
				})
			}

			fmt.Fprintf(w, "shipyard      %s\n", core)
			fmt.Fprintf(w, "shipyard-crew %s\n", addon)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON output")
	return cmd
}
