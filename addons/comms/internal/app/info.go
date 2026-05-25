// Package app holds build-time metadata for shipyard-comms, injected via -ldflags.
package app

import "fmt"

// Build metadata — overridden at link time via:
//
//	-X github.com/shipyard-auto/shipyard/addons/comms/internal/app.Version=<v>
//	-X github.com/shipyard-auto/shipyard/addons/comms/internal/app.Commit=<sha>
//	-X github.com/shipyard-auto/shipyard/addons/comms/internal/app.BuildDate=<date>
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Info returns a human-readable version string.
// Format: shipyard-comms <version> (<commit>, built <buildDate>)
func Info() string {
	return fmt.Sprintf("shipyard-comms %s (%s, built %s)", Version, Commit, BuildDate)
}
