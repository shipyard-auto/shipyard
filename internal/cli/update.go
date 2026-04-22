package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/addon"
	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/crewctl"
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

			if err := updateFairwayIfInstalled(cmd, w); err != nil {
				return err
			}
			return updateCrewIfInstalled(cmd, w)
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
	_ = addon.NewRegistry("").Record(addon.KindFairway, true, inst.BinPath(), inst.Version)

	ui.Printf(w, "%s\n", ui.Emphasis("Fairway updated successfully."))
	return nil
}

func updateCrewIfInstalled(cmd *cobra.Command, w interface{ Write([]byte) (int, error) }) error {
	if _, err := crewctl.ResolveBinary(); err != nil {
		return nil
	}

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Crew Update"))
	ui.Printf(w, "%s\n\n", ui.Muted("Checking the latest crew release..."))

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	latestVersion, err := crewctl.ResolveLatestCrewVersion(cmd.Context(), httpClient)
	if err != nil {
		ui.Printf(w, "%s %v\n", ui.Muted("Could not resolve latest crew version:"), err)
		return nil
	}

	inst, err := buildCrewInstallerForUpdate(latestVersion)
	if err != nil {
		return fmt.Errorf("crew: build installer: %w", err)
	}

	currentVersion, err := inst.InstalledVersion()
	if err != nil {
		currentVersion = "unknown"
	}

	ui.Printf(w, "%s %s\n", ui.Highlight("Current:"), currentVersion)
	ui.Printf(w, "%s %s\n\n", ui.Highlight("Latest:"), latestVersion)

	if err := inst.Upgrade(cmd.Context()); err != nil {
		if errors.Is(err, crewctl.ErrAlreadyAtVersion) {
			ui.Printf(w, "%s\n", ui.Emphasis("Crew is already up to date."))
			return nil
		}
		return err
	}
	_ = addon.NewRegistry("").Record(addon.KindCrew, true, inst.BinPath(), inst.Version)

	ui.Printf(w, "%s\n", ui.Emphasis("Crew updated successfully."))
	return nil
}

// buildCrewInstallerForUpdate builds a production crew Installer for the
// update flow. It mirrors the builder in internal/cli/crew/install.go but
// lives here to avoid leaking an exported constructor just for one caller.
func buildCrewInstallerForUpdate(version string) (*crewctl.Installer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("crew: home dir: %w", err)
	}
	return &crewctl.Installer{
		Version:     version,
		Platform:    crewctl.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		BinDir:      filepath.Join(home, ".local", "bin"),
		StateDir:    filepath.Join(home, ".shipyard", "crew"),
		RunDir:      filepath.Join(home, ".shipyard", "run", "crew"),
		HTTPClient:  crewctl.DefaultHTTPClient(),
		ReleaseBase: crewctl.DefaultReleaseBase,
	}, nil
}
