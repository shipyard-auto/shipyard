// Package app holds build-time metadata for shipyard-fairway, injected via -ldflags.
package app

import "fmt"

// Build metadata — overridden at link time via:
//
//	-X github.com/shipyard-auto/shipyard/addons/fairway/internal/app.Version=<v>
//	-X github.com/shipyard-auto/shipyard/addons/fairway/internal/app.Commit=<sha>
//	-X github.com/shipyard-auto/shipyard/addons/fairway/internal/app.BuildDate=<date>
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Info returns a human-readable version string.
// Format: shipyard-fairway <version> (<commit>, built <buildDate>)
func Info() string {
	return fmt.Sprintf("shipyard-fairway %s (%s, built %s)", Version, Commit, BuildDate)
}
