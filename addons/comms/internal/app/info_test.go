package app_test

import (
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/app"
)

// TestInfo exercises the ldflags-injectable build metadata. We can't easily
// stub package-level vars from outside in parallel-safe ways without races,
// so we keep this test sequential and assert against the default values
// (which is what an unsigned local build produces).
func TestInfo(t *testing.T) {
	got := app.Info()
	wantPrefix := "shipyard-comms "
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("Info() = %q, want prefix %q", got, wantPrefix)
	}
	for _, fragment := range []string{app.Version, app.Commit, app.BuildDate} {
		if !strings.Contains(got, fragment) {
			t.Errorf("Info() = %q, missing %q", got, fragment)
		}
	}
}
