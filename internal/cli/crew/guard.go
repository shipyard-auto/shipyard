package crew

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/crewctl"
)

// ErrAddonNotInstalled is returned when the shipyard-crew binary cannot be
// located on disk.
var ErrAddonNotInstalled = errors.New("shipyard-crew addon is not installed")

// resolveBinaryFn is indirected so tests can substitute a deterministic
// resolver.
var resolveBinaryFn = crewctl.ResolveBinary

// requireInstalled is a cobra PreRunE that fails when the shipyard-crew
// binary is missing. It is attached only to subcommands that need to invoke
// the binary (hire, fire, run); others operate on local state and must work
// without it.
func requireInstalled(cmd *cobra.Command, args []string) error {
	if _, err := resolveBinaryFn(); err != nil {
		return fmt.Errorf("%w: run `shipyard crew install` first", ErrAddonNotInstalled)
	}
	return nil
}
