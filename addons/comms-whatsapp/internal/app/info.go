// Package app holds build-time metadata for shipyard-comms-whatsapp,
// injected via -ldflags.
package app

import "fmt"

// Build metadata — overridden at link time via:
//
//	-X github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/app.Version=<v>
//	-X github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/app.Commit=<sha>
//	-X github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/app.BuildDate=<date>
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Info returns a human-readable version string.
// Format: shipyard-comms-whatsapp <version> (<commit>, built <buildDate>)
func Info() string {
	return fmt.Sprintf("shipyard-comms-whatsapp %s (%s, built %s)", Version, Commit, BuildDate)
}
