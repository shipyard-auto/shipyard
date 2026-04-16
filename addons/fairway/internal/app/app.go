package app

import "fmt"

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func Info() string {
	return fmt.Sprintf("shipyard-fairway %s (%s, built %s)", Version, Commit, BuildDate)
}
