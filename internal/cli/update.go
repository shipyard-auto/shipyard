package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui"
	"github.com/shipyard-auto/shipyard/internal/update"
)

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "update",
		Short:   "Update Shipyard to the latest release",
		Long:    "Download the latest published Shipyard release for this platform and replace the current binary. If shipyard-fairway is installed, it is also updated.",
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

			w := cmd.OutOrStdout()

			ui.Printf(w, "%s\n", ui.SectionTitle("Shipyard Update"))
			ui.Printf(w, "%s\n\n", ui.Muted("Checking the latest release and refreshing your local binary."))

			svc := update.NewService()
			result, err := svc.Run(app.Version, executablePath)
			if err != nil {
				return err
			}

			ui.Printf(w, "%s %s\n", ui.Highlight("Current:"), app.Version)
			ui.Printf(w, "%s %s\n", ui.Highlight("Latest:"), result.LatestVersion)
			ui.Printf(w, "%s %s\n\n", ui.Highlight("Target:"), result.TargetPath)

			if result.Updated {
				ui.Printf(w, "%s\n", ui.Emphasis("Shipyard updated successfully."))
				ui.Printf(w, "%s\n", ui.Muted("Run `shipyard version` to confirm the installed release."))
			} else {
				ui.Printf(w, "%s\n", ui.Emphasis("Shipyard is already up to date."))
			}

			return updateFairwayIfInstalled(cmd, w)
		},
	}
}

func updateFairwayIfInstalled(cmd *cobra.Command, w interface{ Write([]byte) (int, error) }) error {
	sa, err := fairwayctl.NewServiceAdder()
	if err != nil {
		return nil
	}

	installed, err := sa.IsFairwayInstalled()
	if err != nil || !installed {
		return nil
	}

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Fairway Update"))
	ui.Printf(w, "%s\n\n", ui.Muted("Checking the latest fairway release..."))

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	latestVersion, err := fairwayctl.ResolveLatestFairwayVersion(cmd.Context(), httpClient)
	if err != nil {
		ui.Printf(w, "%s %v\n", ui.Muted("Could not resolve latest fairway version:"), err)
		return nil
	}

	inst, err := buildInstaller(latestVersion)
	if err != nil {
		return fmt.Errorf("fairway: build installer: %w", err)
	}

	currentVersion, err := inst.InstalledVersion()
	if err != nil {
		currentVersion = "unknown"
	}

	ui.Printf(w, "%s %s\n", ui.Highlight("Current:"), currentVersion)
	ui.Printf(w, "%s %s\n\n", ui.Highlight("Latest:"), latestVersion)

	if err := inst.Upgrade(cmd.Context()); err != nil {
		if errors.Is(err, fairwayctl.ErrAlreadyAtVersion) {
			ui.Printf(w, "%s\n", ui.Emphasis("Fairway is already up to date."))
			return nil
		}
		return err
	}

	ui.Printf(w, "%s\n", ui.Emphasis("Fairway updated successfully."))
	return nil
}
