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
	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui"
)

func newFairwayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fairway",
		Short: "Manage the shipyard-fairway HTTP gateway addon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newFairwayInstallCmd())
	cmd.AddCommand(newFairwayConfigCmd())
	cmd.AddCommand(newFairwayLogsCmd())
	cmd.AddCommand(newFairwayRouteCmd())
	cmd.AddCommand(newFairwayStatsCmd())
	cmd.AddCommand(newFairwayStatusCmd())
	cmd.AddCommand(newFairwayUninstallCmd())
	return cmd
}

// buildInstaller constructs a production Installer from app.Version and
// the user's home directory. Used by install, uninstall and upgrade commands.
func buildInstaller(version string) (*fairwayctl.Installer, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("fairway: home dir: %w", err)
	}
	sa, err := fairwayctl.NewServiceAdder()
	if err != nil {
		return nil, fmt.Errorf("fairway: service adder: %w", err)
	}
	return &fairwayctl.Installer{
		Version:      version,
		Platform:     fairwayctl.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		BinDir:       filepath.Join(homeDir, ".local", "bin"),
		StateDir:     filepath.Join(homeDir, ".shipyard", "fairway"),
		RunDir:       filepath.Join(homeDir, ".shipyard", "run"),
		HTTPClient:   &http.Client{Timeout: 5 * time.Minute},
		ReleaseBase:  fairwayctl.DefaultReleaseBase,
		Now:          time.Now,
		ServiceAdder: sa,
	}, nil
}

func newFairwayInstallCmd() *cobra.Command {
	return newFairwayInstallCmdWith(nil)
}

// newFairwayInstallCmdWith builds the install command. When installer is
// non-nil it is used directly (tests); otherwise one is built from flags.
func newFairwayInstallCmdWith(installer *fairwayctl.Installer) *cobra.Command {
	var force bool
	var version string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the shipyard-fairway HTTP gateway daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			inst := installer
			if inst == nil {
				if version == "" {
					version = app.Version
				}
				var err error
				inst, err = buildInstaller(version)
				if err != nil {
					return err
				}
			}

			// flags override injected installer fields (test scenarios).
			if installer != nil {
				inst.Force = force
			}

			w := cmd.OutOrStdout()
			ui.Printf(w, "%s\n", ui.SectionTitle("SHIPYARD FAIRWAY"))
			ui.Printf(w, "%s\n\n", ui.Muted(fmt.Sprintf(
				"Installing shipyard-fairway %s for %s/%s...",
				inst.Version, inst.Platform.OS, inst.Platform.Arch,
			)))

			if err := inst.Install(cmd.Context()); err != nil {
				if errors.Is(err, fairwayctl.ErrAlreadyInstalled) {
					ui.Printf(w, "%s\n", ui.Emphasis(
						fmt.Sprintf("fairway %s is already installed.", inst.Version),
					))
					return nil
				}
				if errors.Is(err, fairwayctl.ErrUpgradeRequired) {
					ui.Printf(w, "%s\n", ui.Emphasis("A different version of fairway is installed."))
					ui.Printf(w, "%s\n", ui.Muted("Run 'shipyard update' to update."))
					return err
				}
				return err
			}

			_ = addon.NewRegistry("").Record(addon.KindFairway, true, inst.BinPath(), inst.Version)

			ui.Printf(w, "%s\n", ui.Emphasis("shipyard-fairway installed successfully."))
			ui.Printf(w, "%s\n", ui.Muted("Registered as a Shipyard service — starts automatically on login."))
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Reinstall even if already present")
	cmd.Flags().StringVar(&version, "version", "", "Version to install (default: core version)")
	return cmd
}

func newFairwayUninstallCmd() *cobra.Command {
	return newFairwayUninstallCmdWith(nil)
}

func newFairwayUninstallCmdWith(installer *fairwayctl.Installer) *cobra.Command {
	var purge bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the shipyard-fairway daemon and deregister its service",
		RunE: func(cmd *cobra.Command, args []string) error {
			inst := installer
			if inst == nil {
				var err error
				inst, err = buildInstaller(app.Version)
				if err != nil {
					return err
				}
			}
			inst.Purge = purge

			w := cmd.OutOrStdout()
			ui.Printf(w, "%s\n", ui.SectionTitle("SHIPYARD FAIRWAY"))
			ui.Printf(w, "%s\n\n", ui.Muted("Removing shipyard-fairway..."))

			if err := inst.Uninstall(cmd.Context()); err != nil {
				return err
			}
			_ = addon.NewRegistry("").Forget(addon.KindFairway)

			ui.Printf(w, "%s\n", ui.Emphasis("shipyard-fairway removed."))
			if purge {
				ui.Printf(w, "%s\n", ui.Muted("State directory purged."))
			} else {
				ui.Printf(w, "%s\n", ui.Muted("State preserved in "+inst.StateDir))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&purge, "purge", false, "Also remove ~/.shipyard/fairway/ (routes, config, logs)")
	return cmd
}
