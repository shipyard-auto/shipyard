package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/uninstall"
)

func newUninstallCmd() *cobra.Command {
	var assumeYes bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Shipyard completely from this machine",
		Long:  "Delete the Shipyard binary and the ~/.shipyard directory created during installation.",
		Example: strings.Join([]string{
			"shipyard uninstall",
			"shipyard uninstall --yes",
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
