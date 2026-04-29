package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/addon"
	"github.com/shipyard-auto/shipyard/internal/uninstall"
)

// uninstallAddonFunc desinstala um addon específico. Variável package-level
// para permitir override em testes. NÃO exportar — mantém o blast radius
// pequeno.
type uninstallAddonFunc func(ctx context.Context) error

// loadInstalledAddons devolve a lista (ordenada e estável) de addons marcados
// como instalados no registry ~/.shipyard/addons.json. Variável para testes.
var loadInstalledAddons = defaultLoadInstalledAddons

// uninstallCrewAddon executa o uninstall do crew. Variável para testes.
var uninstallCrewAddon uninstallAddonFunc = defaultUninstallCrewAddon

// uninstallFairwayAddon executa o uninstall do fairway. Variável para testes.
var uninstallFairwayAddon uninstallAddonFunc = defaultUninstallFairwayAddon

func newUninstallCmd() *cobra.Command {
	var assumeYes bool
	var keepAddons bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Shipyard completely from this machine",
		Long: `Delete the Shipyard binary and the ~/.shipyard directory created during
installation. Detected addons (crew, fairway) are uninstalled in cascade by
default; pass --keep-addons to leave them in place.`,
		Example: strings.Join([]string{
			"shipyard uninstall",
			"shipyard uninstall --yes",
			"shipyard uninstall --yes --keep-addons",
		}, "\n"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			executablePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve current executable: %w", err)
			}

			executablePath, err = filepath.EvalSymlinks(executablePath)
			if err != nil {
				executablePath = filepath.Clean(executablePath)
			}

			if !assumeYes {
				ok, err := confirm(cmd.OutOrStdout())
				if err != nil {
					return err
				}
				if !ok {
					PrintResult(cmd.OutOrStdout(), "Uninstall cancelled.\n")
					return nil
				}
			}

			if !keepAddons {
				cascadeUninstallAddons(cmd)
			}

			service := uninstall.NewService()
			result, err := service.Run(executablePath)
			if err != nil {
				return err
			}

			PrintResult(cmd.OutOrStdout(), "Removed Shipyard binary: %s\n", removedLabel(result.BinaryRemoved, result.BinaryPath))
			PrintResult(cmd.OutOrStdout(), "Removed Shipyard home: %s\n", removedLabel(result.HomeDirRemoved, result.HomeDir))
			if !result.ManifestPresent {
				PrintResult(cmd.OutOrStdout(), "Install manifest was not found. Used the current binary path as fallback.\n")
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Run without interactive confirmation")
	cmd.Flags().BoolVar(&keepAddons, "keep-addons", false, "Skip uninstalling detected addons (crew, fairway)")

	return cmd
}

func confirm(out io.Writer) (bool, error) {
	reader := bufio.NewReader(os.Stdin)
	if _, err := fmt.Fprint(out, "This will completely remove Shipyard from this machine. Continue? [y/N]: "); err != nil {
		return false, fmt.Errorf("write confirmation prompt: %w", err)
	}

	answer, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("read confirmation prompt: %w", err)
	}

	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes", nil
}

func removedLabel(removed bool, path string) string {
	if removed {
		return path
	}

	return path + " (not found)"
}

// cascadeUninstallAddons uninstalls every addon currently marked as installed
// in the registry. Failures are reported as warnings on stdout — they never
// block the core uninstall, because by the time the user typed `shipyard
// uninstall` the intent is already to wipe the machine.
func cascadeUninstallAddons(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	kinds := loadInstalledAddons()
	if len(kinds) == 0 {
		return
	}

	var errs []error
	for _, kind := range kinds {
		var fn uninstallAddonFunc
		switch kind {
		case addon.KindCrew:
			fn = uninstallCrewAddon
		case addon.KindFairway:
			fn = uninstallFairwayAddon
		default:
			// Unknown kinds: log and skip. Registry may be from a future
			// shipyard version; refusing here would block a legitimate
			// uninstall.
			PrintResult(out, "Skipped unknown addon: %s\n", kind)
			continue
		}
		if err := fn(cmd.Context()); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", kind, err))
			PrintResult(out, "Warning: failed to uninstall %s addon: %v\n", kind, err)
			continue
		}
		_ = addon.NewRegistry("").Forget(kind)
		PrintResult(out, "Removed addon: %s\n", kind)
	}

	if joined := errors.Join(errs...); joined != nil {
		// Print summary; do NOT return — the core uninstall must proceed.
		PrintResult(out, "Some addons could not be removed cleanly. Re-run their individual uninstall commands manually if needed.\n")
	}
}

// defaultLoadInstalledAddons reads the addon registry and returns the kinds
// marked as installed, sorted alphabetically for deterministic output.
func defaultLoadInstalledAddons() []addon.Kind {
	reg := addon.NewRegistry("")
	file, err := reg.Load()
	if err != nil || file == nil {
		return nil
	}
	out := make([]addon.Kind, 0, len(file.Addons))
	for kind, info := range file.Addons {
		if info != nil && info.Installed {
			out = append(out, kind)
		}
	}
	// Sort by string value for determinism (test stability).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// defaultUninstallCrewAddon builds the production crew installer and runs
// Uninstall on it. The Version field is irrelevant for uninstall, so we pass
// an empty string.
func defaultUninstallCrewAddon(ctx context.Context) error {
	inst, err := buildCrewInstallerForUpdate("")
	if err != nil {
		return err
	}
	return inst.Uninstall(ctx)
}

// defaultUninstallFairwayAddon builds the production fairway installer and
// runs Uninstall on it.
func defaultUninstallFairwayAddon(ctx context.Context) error {
	inst, err := buildInstaller("")
	if err != nil {
		return err
	}
	return inst.Uninstall(ctx)
}
