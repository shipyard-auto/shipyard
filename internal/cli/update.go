package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/ui"
	"github.com/shipyard-auto/shipyard/internal/update"
)

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "update",
		Short:   "Update Shipyard to the latest release",
		Long:    "Download the latest published Shipyard release for this platform and replace the current binary.",
		Example: "shipyard update",
		RunE: func(cmd *cobra.Command, _ []string) error {
			executablePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve current executable: %w", err)
			}

			executablePath, err = filepath.EvalSymlinks(executablePath)
			if err != nil {
				executablePath = filepath.Clean(executablePath)
			}

			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.SectionTitle("Shipyard Update"))
			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Muted("Checking the latest release and refreshing your local binary."))
			ui.Printf(cmd.OutOrStdout(), "\n")

			service := update.NewService()
			result, err := service.Run(app.Version, executablePath)
			if err != nil {
				return err
			}

			ui.Printf(cmd.OutOrStdout(), "%s %s\n", ui.Highlight("Current:"), app.Version)
			ui.Printf(cmd.OutOrStdout(), "%s %s\n", ui.Highlight("Latest:"), result.LatestVersion)
			ui.Printf(cmd.OutOrStdout(), "%s %s\n", ui.Highlight("Target:"), result.TargetPath)
			ui.Printf(cmd.OutOrStdout(), "\n")

			if !result.Updated {
				ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("Shipyard is already up to date."))
				return nil
			}

			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("Shipyard updated successfully."))
			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Muted("Run `shipyard version` to confirm the installed release."))
			return nil
		},
	}
}
