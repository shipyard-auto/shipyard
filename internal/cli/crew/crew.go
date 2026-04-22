package crew

import (
	"github.com/spf13/cobra"
)

// NewCrewCmd constructs the root `shipyard crew` command and wires every
// subcommand. The order of AddCommand calls determines the order in --help.
func NewCrewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crew",
		Short: "Manage the shipyard-crew LLM agents addon",
		Long:  "shipyard crew lets you create, configure, and run LLM-backed agents that automate tasks.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	runCmd := NewRunCmd()
	runCmd.PreRunE = requireInstalled

	cmd.AddCommand(NewInstallCmd())
	cmd.AddCommand(NewUninstallCmd())
	cmd.AddCommand(NewVersionCmd())
	cmd.AddCommand(newHireCmd())
	cmd.AddCommand(newFireCmd())
	cmd.AddCommand(newApplyCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(runCmd)
	cmd.AddCommand(newLogsCmd())

	return cmd
}
