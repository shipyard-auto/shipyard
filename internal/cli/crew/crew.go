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
		Long: `Crew is the AI agent subsystem of Shipyard. Use it to define LLM-backed agents
that automate tasks on a schedule or in response to HTTP webhooks. Agent
definitions live under ~/.shipyard/crew/<name>/ and are registered with the
running daemon via "shipyard crew apply".`,
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
	cmd.AddCommand(newToolCmd())

	return cmd
}
