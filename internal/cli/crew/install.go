package crew

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	versiondata "github.com/shipyard-auto/shipyard"
	"github.com/shipyard-auto/shipyard/internal/addon"
	"github.com/shipyard-auto/shipyard/internal/crewctl"
	"github.com/shipyard-auto/shipyard/internal/ui"
)

// NewInstallCmd returns the `shipyard crew install` subcommand.
func NewInstallCmd() *cobra.Command {
	return newInstallCmdWith(nil)
}

// newInstallCmdWith builds the install command. When inst is non-nil it is
// reused directly (test seam); otherwise a production installer is built
// from flag values.
func newInstallCmdWith(inst *crewctl.Installer) *cobra.Command {
	var force bool
	var version string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Download and install the AI agent runtime",
		Long: `Downloads the shipyard-crew binary for the current platform and installs it to
~/.local/bin/. Once installed, the agent runtime is available but no agents
are active until you run "shipyard crew hire" and "shipyard crew apply". Use
--force to reinstall an already-present version.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target := inst
			if target == nil {
				v := version
				if v == "" {
					v = versiondata.ComponentVersion("crew")
				}
				var err error
				target, err = crewInstallerBuilder(v)
				if err != nil {
					return err
				}
			} else if version != "" {
				target.Version = version
			}
			target.Force = force

			w := cmd.OutOrStdout()
			ui.Printf(w, "%s\n", ui.SectionTitle("SHIPYARD CREW"))
			ui.Printf(w, "%s\n\n", ui.Muted(fmt.Sprintf(
				"Installing shipyard-crew %s for %s/%s...",
				target.Version, target.Platform.OS, target.Platform.Arch,
			)))

			if err := target.Install(cmd.Context()); err != nil {
				if errors.Is(err, crewctl.ErrAlreadyInstalled) {
					ui.Printf(w, "%s\n", ui.Emphasis(
						fmt.Sprintf("shipyard-crew %s is already installed.", target.Version),
					))
					return nil
				}
				if errors.Is(err, crewctl.ErrUpgradeRequired) {
					ui.Printf(w, "%s\n", ui.Emphasis("A different version of shipyard-crew is installed."))
					ui.Printf(w, "%s\n", ui.Muted("Re-run with --force to reinstall."))
					return err
				}
				return err
			}

			_ = addon.NewRegistry("").Record(addon.KindCrew, true, target.BinPath(), target.Version)

			ui.Printf(w, "%s\n", ui.Emphasis("shipyard-crew installed successfully."))
			ui.Printf(w, "%s\n", ui.Muted("installed: "+target.BinPath()))
			maybeWarnPATH(cmd.ErrOrStderr(), os.Getenv, target.BinDir)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Reinstall even if already present")
	cmd.Flags().StringVar(&version, "version", "", "Version to install (default: version from manifest)")
	return cmd
}

// maybeWarnPATH inspects the user's $PATH and emits a stderr hint when the
// install target (binDir) is not present. The message is shell-aware:
// bash/zsh receive an `export PATH=...` snippet plus the matching rc-file
// reference, fish gets `fish_add_path`, and any other shell falls back to
// a generic instruction. binDir must be the absolute install directory
// (Installer.BinDir). Tests inject getenv so the check is hermetic.
func maybeWarnPATH(stderr io.Writer, getenv func(string) string, binDir string) {
	if binDir == "" {
		return
	}
	target := strings.TrimRight(filepath.Clean(binDir), string(os.PathSeparator))
	for _, entry := range filepath.SplitList(getenv("PATH")) {
		if strings.TrimRight(filepath.Clean(entry), string(os.PathSeparator)) == target {
			return
		}
	}

	shell := filepath.Base(getenv("SHELL"))
	var hint string
	switch shell {
	case "bash":
		hint = fmt.Sprintf("  echo 'export PATH=\"%s:$PATH\"' >> ~/.bashrc && source ~/.bashrc", binDir)
	case "zsh":
		hint = fmt.Sprintf("  echo 'export PATH=\"%s:$PATH\"' >> ~/.zshrc && source ~/.zshrc", binDir)
	case "fish":
		hint = fmt.Sprintf("  fish_add_path %s", binDir)
	default:
		hint = fmt.Sprintf("  add %s to your shell's PATH", binDir)
	}

	fmt.Fprintf(stderr, "warning: %s is not in your PATH — `shipyard-crew` won't be found by your shell.\n", binDir)
	fmt.Fprintf(stderr, "to fix:\n%s\n", hint)
}

// crewInstallerBuilder is indirected so tests can substitute a builder that
// returns a fake installer instead of reaching for the real network.
var crewInstallerBuilder = buildCrewInstaller

// buildCrewInstaller constructs a production Installer from the given version,
// the current platform and the user's home directory.
func buildCrewInstaller(version string) (*crewctl.Installer, error) {
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
		LogsDir:     filepath.Join(home, ".shipyard", "logs", "crew"),
		HTTPClient:  crewctl.DefaultHTTPClient(),
		ReleaseBase: crewctl.DefaultReleaseBase,
	}, nil
}
