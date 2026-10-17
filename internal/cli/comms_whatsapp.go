package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// whatsappProgName is what the addon's help shows for itself when reached
// through this command (the addon reads SHIPYARD_PROG).
const whatsappProgName = "shipyard comms whatsapp"

// newCommsWhatsAppCmd is `shipyard comms whatsapp`. install, update and
// uninstall are handled here (they manage the binary); every other
// subcommand is passed through to shipyard-comms-whatsapp verbatim, whose
// own `help` is the reference.
func newCommsWhatsAppCmd() *cobra.Command {
	return newCommsWhatsAppCmdWith(commsPassthroughDeps{}, nil)
}

func newCommsWhatsAppCmdWith(deps commsPassthroughDeps, installer *commsctl.Installer) *cobra.Command {
	deps.Binary = commsctl.WhatsApp.Binary
	deps.InstallHint = "shipyard comms whatsapp install"
	deps.Env = append(deps.Env, "SHIPYARD_PROG="+whatsappProgName)
	if deps.ResolveBinary == nil {
		deps.ResolveBinary = commsctl.WhatsApp.ResolveBinary
	}

	cmd := &cobra.Command{
		Use:   "whatsapp",
		Short: "WhatsApp provider: linked device, archive, policy-checked sending",
		Long: `Runs shipyard-comms-whatsapp, the comms provider that links this machine to a
WhatsApp account as a companion device, archives watched chats with a TTL,
delivers received messages to fairway and sends only through a daemon that
enforces an allowlist, a prior-conversation rule and rate limits.

  shipyard comms whatsapp install | update | uninstall [--purge]
  shipyard comms whatsapp help                 # full reference (for agents too)
  shipyard comms whatsapp <command> [flags]    # pair, serve, status, chats, send, …

Everything except install/update/uninstall is handled by the addon binary.`,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"help"}
			}
			return runCommsPassthrough(cmd.Context(), deps, args)
		},
	}
	cmd.AddCommand(newWhatsAppInstallCmd(installer))
	cmd.AddCommand(newWhatsAppUpdateCmd(installer))
	cmd.AddCommand(newWhatsAppUninstallCmd(installer))
	return cmd
}

// buildWhatsAppInstaller builds the production installer for version;
// "dev" or "" (no pin in the manifest) resolves the latest release.
func buildWhatsAppInstaller(ctx context.Context, version string) (*commsctl.Installer, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("comms-whatsapp: home dir: %w", err)
	}
	httpClient := &http.Client{Timeout: 5 * time.Minute}
	if version == "" || version == "dev" {
		if version, err = commsctl.ResolveLatestVersion(ctx, httpClient, commsctl.WhatsApp); err != nil {
			return nil, fmt.Errorf("comms-whatsapp: resolve latest version: %w", err)
		}
	}
	return &commsctl.Installer{
		Version:     version,
		Platform:    commsctl.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		BinDir:      filepath.Join(homeDir, ".local", "bin"),
		StateDir:    filepath.Join(homeDir, ".shipyard", "comms", commsctl.WhatsApp.StateSubdir),
		HTTPClient:  httpClient,
		ReleaseBase: commsctl.DefaultReleaseBase,
		Now:         time.Now,
		Product:     commsctl.WhatsApp,
	}, nil
}

func whatsappHeader(w io.Writer) { ui.Printf(w, "%s\n", ui.SectionTitle("SHIPYARD COMMS · WHATSAPP")) }

func newWhatsAppInstallCmd(installer *commsctl.Installer) *cobra.Command {
	var force bool
	var version string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the shipyard-comms-whatsapp binary",
		Long: `Downloads shipyard-comms-whatsapp for this platform into ~/.local/bin/ and
verifies its checksum. Nothing is paired or started: next run
'shipyard comms whatsapp pair', then run 'serve' under 'shipyard service'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			inst := installer
			if inst == nil {
				if version == "" {
					version = versiondata.ComponentVersion("comms-whatsapp")
				}
				var err error
				if inst, err = buildWhatsAppInstaller(cmd.Context(), version); err != nil {
					return err
				}
			}
			inst.Force = force
			w := cmd.OutOrStdout()
			whatsappHeader(w)
			ui.Printf(w, "%s\n\n", ui.Muted(fmt.Sprintf("Installing shipyard-comms-whatsapp %s for %s/%s...",
				inst.Version, inst.Platform.OS, inst.Platform.Arch)))
			if err := inst.Install(cmd.Context()); err != nil {
				switch {
				case errors.Is(err, commsctl.ErrAlreadyInstalled):
					ui.Printf(w, "%s\n", ui.Emphasis(fmt.Sprintf("shipyard-comms-whatsapp %s is already installed.", inst.Version)))
					return nil
				case errors.Is(err, commsctl.ErrUpgradeRequired):
					ui.Printf(w, "%s\n", ui.Muted("A different version is installed: run 'shipyard comms whatsapp update'."))
				}
				return err
			}
			_ = addon.NewRegistry("").Record(addon.KindCommsWhatsApp, true, inst.BinPath(), inst.Version)
			ui.Printf(w, "%s\n", ui.Emphasis("shipyard-comms-whatsapp installed."))
			ui.Printf(w, "%s\n", ui.Muted("Next: 'shipyard comms whatsapp pair', then 'shipyard comms whatsapp help'."))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Reinstall even if already present")
	cmd.Flags().StringVar(&version, "version", "", "Version to install (default: manifest pin, else latest release)")
	return cmd
}

func newWhatsAppUpdateCmd(installer *commsctl.Installer) *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update shipyard-comms-whatsapp to the latest release",
		Long: `Replaces the binary with the latest release (or --version). State under
~/.shipyard/comms/whatsapp/ — session keys, archive, config — is kept.
Restart the daemon's service afterwards so the new binary takes over.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			inst := installer
			if inst == nil {
				var err error
				if inst, err = buildWhatsAppInstaller(cmd.Context(), version); err != nil {
					return err
				}
			}
			w := cmd.OutOrStdout()
			whatsappHeader(w)
			current, err := inst.InstalledVersion()
			if err != nil {
				current = "unknown"
			}
			ui.Printf(w, "%s %s\n%s %s\n\n", ui.Highlight("Current:"), current, ui.Highlight("Target:"), inst.Version)
			if err := inst.Upgrade(cmd.Context()); err != nil {
				if errors.Is(err, commsctl.ErrAlreadyAtVersion) {
					ui.Printf(w, "%s\n", ui.Emphasis("shipyard-comms-whatsapp is already up to date."))
					return nil
				}
				return err
			}
			_ = addon.NewRegistry("").Record(addon.KindCommsWhatsApp, true, inst.BinPath(), inst.Version)
			ui.Printf(w, "%s\n", ui.Emphasis("shipyard-comms-whatsapp updated."))
			ui.Printf(w, "%s\n", ui.Muted("Restart its service ('shipyard service restart <id>') to run the new version."))
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "Pin to a specific version instead of the latest")
	return cmd
}

func newWhatsAppUninstallCmd(installer *commsctl.Installer) *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the shipyard-comms-whatsapp binary",
		Long: `Removes the binary. The linked-device session, archive and config under
~/.shipyard/comms/whatsapp/ are kept unless --purge. Purging deletes the
session keys locally but does not unlink the device on the phone: run
'shipyard comms whatsapp logout' first, or remove it under "Linked devices".
Stop the daemon's service before uninstalling.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			inst := installer
			if inst == nil {
				var err error
				if inst, err = buildWhatsAppInstaller(cmd.Context(), versiondata.ComponentVersion("comms-whatsapp")); err != nil {
					// Uninstall must work offline: the version is irrelevant.
					inst, err = whatsappInstallerNoVersion()
					if err != nil {
						return err
					}
				}
			}
			inst.Purge = purge
			w := cmd.OutOrStdout()
			whatsappHeader(w)
			if err := inst.Uninstall(cmd.Context()); err != nil {
				return err
			}
			_ = addon.NewRegistry("").Forget(addon.KindCommsWhatsApp)
			ui.Printf(w, "%s\n", ui.Emphasis("shipyard-comms-whatsapp removed."))
			if purge {
				ui.Printf(w, "%s\n", ui.Muted("Session, archive and config deleted from "+inst.StateDir))
			} else {
				ui.Printf(w, "%s\n", ui.Muted("Session, archive and config kept in "+inst.StateDir))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "Also delete ~/.shipyard/comms/whatsapp/ (session keys, archive, config)")
	return cmd
}

func whatsappInstallerNoVersion() (*commsctl.Installer, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &commsctl.Installer{
		BinDir:   filepath.Join(homeDir, ".local", "bin"),
		StateDir: filepath.Join(homeDir, ".shipyard", "comms", commsctl.WhatsApp.StateSubdir),
		Product:  commsctl.WhatsApp,
	}, nil
}
