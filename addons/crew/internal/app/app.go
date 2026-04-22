// Package app expose build metadata for the shipyard-crew addon binary.
//
// The values of Version, Commit and BuildDate are injected at build time
// through -ldflags -X (see Makefile target build-crew).
package app

import "fmt"

var (
	// Version is the semantic version of shipyard-crew, read from the
	// root-level `manifest` file and injected via ldflags.
	Version = "dev"

	// Commit is the short git commit the binary was built from.
	Commit = "unknown"

	// BuildDate is the ISO-8601 date the binary was built.
	BuildDate = "unknown"
)

// Info returns a human-friendly single-line string describing this build.
// Format: "shipyard-crew <version> (<commit>, built <date>)".
func Info() string {
	return fmt.Sprintf("shipyard-crew %s (%s, built %s)", Version, Commit, BuildDate)
}
