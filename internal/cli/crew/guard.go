package crew

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/addon"
	"github.com/shipyard-auto/shipyard/internal/crewctl"
)

// ErrAddonNotInstalled is returned when the shipyard-crew binary cannot be
// located on disk. The error wraps addon.ErrNotInstalled so callers who only
// care about the unified sentinel can match either one.
var ErrAddonNotInstalled = fmt.Errorf("shipyard-crew addon is not installed: %w", addon.ErrNotInstalled)

// resolveBinaryFn is indirected so tests can substitute a deterministic
// resolver.
var resolveBinaryFn = crewctl.ResolveBinary

// requireInstalled is the cobra PreRunE attached to crew subcommands that
// invoke the binary. It keeps using resolveBinaryFn so tests can override the
// resolver, and returns an error chain that is_errors.Is compatible with both
// the legacy ErrAddonNotInstalled and the unified addon.ErrNotInstalled.
func requireInstalled(cmd *cobra.Command, args []string) error {
	if _, err := resolveBinaryFn(); err != nil {
		return fmt.Errorf("%w: run `shipyard crew install` first", ErrAddonNotInstalled)
	}
	return nil
}
