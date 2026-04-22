package addon

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
)

// ErrNotInstalled is the sentinel returned by RequirePreRun when an addon
// binary cannot be found. Callers can match on errors.Is to distinguish
// between missing addons and other failures.
var ErrNotInstalled = errors.New("addon is not installed")

// detectFn is indirected so tests can substitute a deterministic detector
// without constructing a Registry instance.
var detectFn func(Kind) (Info, error) = func(k Kind) (Info, error) {
	return defaultDetector{}.Detect(k)
}

// RequirePreRun returns a Cobra PreRunE that fails with a friendly message
// when the addon binary is missing. The returned function is safe to share
// across commands.
func RequirePreRun(kind Kind) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		info, err := detectFn(kind)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrNotInstalled, kind, err)
		}
		if !info.Installed {
			return fmt.Errorf("%w: %s — run `%s` first", ErrNotInstalled, kind.BinaryName(), kind.InstallCommand())
		}
		return nil
	}
}

// WithTestDetector swaps the package-level detector for the duration of a
// test. It is intended for external test packages that exercise commands
// guarded by RequirePreRun and need to pretend the addon is installed (or
// missing) without touching the filesystem. The previous detector is
// restored automatically via t.Cleanup.
func WithTestDetector(t *testing.T, fn func(Kind) (Info, error)) {
	t.Helper()
	prev := detectFn
	detectFn = fn
	t.Cleanup(func() { detectFn = prev })
}

// SetDetectorForTest swaps the package-level detector and returns a restore
// function. It is intended for package-level test setup (TestMain) that
// cannot call t.Cleanup. Production code must never call this.
func SetDetectorForTest(fn func(Kind) (Info, error)) (restore func()) {
	prev := detectFn
	detectFn = fn
	return func() { detectFn = prev }
}

// AlwaysInstalledDetector returns a detector that reports every Kind as
// installed at a stub path. Useful for tests that only care about command
// wiring downstream of RequirePreRun.
func AlwaysInstalledDetector() func(Kind) (Info, error) {
	return func(k Kind) (Info, error) {
		return Info{Kind: k, Installed: true, BinaryPath: "/stub/" + k.BinaryName()}, nil
	}
}
