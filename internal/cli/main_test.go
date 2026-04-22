package cli

import (
	"os"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/addon"
)

// TestMain stubs the addon detector so commands guarded by
// addon.RequirePreRun do not need the real binaries to be present on the
// test host. Tests that need to exercise the "addon missing" path can still
// override detectFn locally with addon.WithTestDetector.
func TestMain(m *testing.M) {
	restore := addon.SetDetectorForTest(addon.AlwaysInstalledDetector())
	code := m.Run()
	restore()
	os.Exit(code)
}
