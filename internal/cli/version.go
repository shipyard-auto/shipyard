package cli

import (
	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/app"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show Shipyard version information",
		Long:  "Display the current Shipyard version, commit, and build date.",
		Run: func(cmd *cobra.Command, _ []string) {
			PrintResult(cmd.OutOrStdout(), "Shipyard\n")
			PrintResult(cmd.OutOrStdout(), "  Version:    %s\n", app.Version)
			PrintResult(cmd.OutOrStdout(), "  Commit:     %s\n", app.Commit)
			PrintResult(cmd.OutOrStdout(), "  Build Date: %s\n", app.BuildDate)
		},
	}
}
