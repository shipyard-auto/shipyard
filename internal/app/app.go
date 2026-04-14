package app

import versiondata "github.com/shipyard-auto/shipyard"

const (
	Name        = "shipyard"
	Description = "Install and operate Shipyard from the terminal."
)

var (
	Version   = versiondata.CurrentVersion()
	Commit    = "dev"
	BuildDate = "unknown"
)
