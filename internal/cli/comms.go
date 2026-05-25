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

	versiondata "github.com/shipyard-auto/shipyard"
	"github.com/shipyard-auto/shipyard/internal/addon"
	"github.com/shipyard-auto/shipyard/internal/commsctl"
	"github.com/shipyard-auto/shipyard/internal/ui"
)

func newCommsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "comms",
		Short: "Manage the shipyard-comms external messaging addon",
		Long: `Comms is the transport layer for external messaging in Shipyard. It owns the
catalog of channels (Telegram, Slack, WhatsApp, …), credentials and the
normalized Message envelope used across the subsystem. Inbound webhook
traffic enters through fairway; comms exposes the send operation for any
caller (scripts, cron jobs, crew agents, fairway actions).

In v1 comms is stateless: each invocation reads config from
~/.shipyard/comms/, performs one operation and exits. No daemon, no socket.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newCommsInstallCmd())
	cmd.AddCommand(newCommsUpdateCmd())
	cmd.AddCommand(newCommsUninstallCmd())
	cmd.AddCommand(newCommsStatusCmd())
	cmd.AddCommand(newCommsChannelCmd())
	cmd.AddCommand(newCommsSendCmd())
	return cmd
}

func newCommsUpdateCmd() *cobra.Command {
	return newCommsUpdateCmdWith(nil)
}

// newCommsUpdateCmdWith builds the update command. When installer is
// non-nil it is used directly (tests); otherwise one is built fresh per
// invocation, with the target version resolved from the GitHub API
// unless --version pins it.
func newCommsUpdateCmdWith(installer *commsctl.Installer) *cobra.Command {
	var version string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update shipyard-comms to the latest release",
		Long: `Downloads the newest published comms release (or the version pinned via
--version) and reinstalls in place. State under ~/.shipyard/comms/ is
preserved — only the binary is replaced. When the installed version
already matches the target, the command reports "already up to date"
and exits 0.

This is functionally equivalent to running:
    shipyard comms install --force [--version X]

The 'update' wrapper is the recommended path because it uses the
installer's Upgrade flow (clean uninstall + install) and resolves the
latest version automatically when --version is omitted.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			inst := installer
			if inst == nil {
				target := version
				if target == "" {
					ctx := cmd.Context()
					httpClient := &http.Client{Timeout: 5 * time.Minute}
					resolved, err := commsctl.ResolveLatestCommsVersion(ctx, httpClient)
					if err != nil {
						return fmt.Errorf("comms: resolve latest version: %w", err)
					}
					target = resolved
				}
				var err error
				inst, err = buildCommsInstaller(target)
				if err != nil {
					return err
				}
			}

			w := cmd.OutOrStdout()
			ui.Printf(w, "%s\n", ui.SectionTitle("SHIPYARD COMMS"))

			currentVersion, err := inst.InstalledVersion()
			if err != nil {
				currentVersion = "unknown"
			}
			ui.Printf(w, "%s %s\n", ui.Highlight("Current:"), currentVersion)
			ui.Printf(w, "%s %s\n\n", ui.Highlight("Target:"), inst.Version)

			if err := inst.Upgrade(cmd.Context()); err != nil {
				if errors.Is(err, commsctl.ErrAlreadyAtVersion) {
					ui.Printf(w, "%s\n", ui.Emphasis("Comms is already up to date."))
					return nil
				}
				return err
			}
			_ = addon.NewRegistry("").Record(addon.KindComms, true, inst.BinPath(), inst.Version)

			ui.Printf(w, "%s\n", ui.Emphasis("shipyard-comms updated successfully."))
			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Pin to a specific version instead of resolving the latest")
	return cmd
}

// buildCommsInstaller constructs a production Installer from the given comms
// version and the user's home directory. Used by install, uninstall and
// upgrade commands. Mirrors buildInstaller (fairway) and
// buildCrewInstallerForUpdate (crew).
func buildCommsInstaller(version string) (*commsctl.Installer, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("comms: home dir: %w", err)
	}
	return &commsctl.Installer{
		Version:     version,
		Platform:    commsctl.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		BinDir:      filepath.Join(homeDir, ".local", "bin"),
		StateDir:    filepath.Join(homeDir, ".shipyard", "comms"),
		HTTPClient:  &http.Client{Timeout: 5 * time.Minute},
		ReleaseBase: commsctl.DefaultReleaseBase,
		Now:         time.Now,
	}, nil
}

func newCommsInstallCmd() *cobra.Command {
	return newCommsInstallCmdWith(nil)
}

// newCommsInstallCmdWith builds the install command. When installer is
// non-nil it is used directly (tests); otherwise one is built from flags.
func newCommsInstallCmdWith(installer *commsctl.Installer) *cobra.Command {
	var force bool
	var version string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the shipyard-comms messaging addon",
		Long: `Downloads the shipyard-comms binary for the current platform and places it
under ~/.local/bin/. Comms is stateless — no service is registered.
Channel configuration and credentials live under ~/.shipyard/comms/ and
are created on demand by subsequent ` + "`shipyard comms`" + ` commands. Use
--version to pin a specific release or --force to reinstall an existing one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			inst := installer
			if inst == nil {
				if version == "" {
					version = versiondata.ComponentVersion("comms")
				}
				var err error
				inst, err = buildCommsInstaller(version)
				if err != nil {
					return err
				}
			}

			// Tests inject the installer directly; honor the flag in that path.
			if installer != nil {
				inst.Force = force
			} else {
				inst.Force = force
			}

			w := cmd.OutOrStdout()
			ui.Printf(w, "%s\n", ui.SectionTitle("SHIPYARD COMMS"))
			ui.Printf(w, "%s\n\n", ui.Muted(fmt.Sprintf(
				"Installing shipyard-comms %s for %s/%s...",
				inst.Version, inst.Platform.OS, inst.Platform.Arch,
			)))

			if err := inst.Install(cmd.Context()); err != nil {
				if errors.Is(err, commsctl.ErrAlreadyInstalled) {
					ui.Printf(w, "%s\n", ui.Emphasis(
						fmt.Sprintf("comms %s is already installed.", inst.Version),
					))
					return nil
				}
				if errors.Is(err, commsctl.ErrUpgradeRequired) {
					ui.Printf(w, "%s\n", ui.Emphasis("A different version of comms is installed."))
					ui.Printf(w, "%s\n", ui.Muted("Run 'shipyard update' to update."))
					return err
				}
				return err
			}

			_ = addon.NewRegistry("").Record(addon.KindComms, true, inst.BinPath(), inst.Version)

			ui.Printf(w, "%s\n", ui.Emphasis("shipyard-comms installed successfully."))
			ui.Printf(w, "%s\n", ui.Muted("Run 'shipyard comms status' to confirm."))
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Reinstall even if already present")
	cmd.Flags().StringVar(&version, "version", "", "Version to install (default: version from manifest)")
	return cmd
}

func newCommsUninstallCmd() *cobra.Command {
	return newCommsUninstallCmdWith(nil)
}

func newCommsUninstallCmdWith(installer *commsctl.Installer) *cobra.Command {
	var purge bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the shipyard-comms binary",
		Long: `Removes the shipyard-comms binary from ~/.local/bin/. Channel configuration
and credentials under ~/.shipyard/comms/ are preserved by default; use
--purge to delete them as well.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			inst := installer
			if inst == nil {
				var err error
				inst, err = buildCommsInstaller(versiondata.ComponentVersion("comms"))
				if err != nil {
					return err
				}
			}
			inst.Purge = purge

			w := cmd.OutOrStdout()
			ui.Printf(w, "%s\n", ui.SectionTitle("SHIPYARD COMMS"))
			ui.Printf(w, "%s\n\n", ui.Muted("Removing shipyard-comms..."))

			if err := inst.Uninstall(cmd.Context()); err != nil {
				return err
			}
			_ = addon.NewRegistry("").Forget(addon.KindComms)

			ui.Printf(w, "%s\n", ui.Emphasis("shipyard-comms removed."))
			if purge {
				ui.Printf(w, "%s\n", ui.Muted("State directory purged."))
			} else {
				ui.Printf(w, "%s\n", ui.Muted("State preserved in "+inst.StateDir))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&purge, "purge", false, "Also remove ~/.shipyard/comms/ (channels, credentials)")
	return cmd
}
