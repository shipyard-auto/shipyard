package versiondata

import (
	_ "embed"
	"strings"
)

//go:embed manifest
var rawManifest string

func CurrentVersion() string {
	return ComponentVersion("shipyard")
}

// ComponentVersion returns the version pinned in the embedded manifest for the
// given component name (e.g. "shipyard", "fairway", "crew"). When the
// component is missing or empty, it returns "dev".
func ComponentVersion(name string) string {
	prefix := name + "="
	for _, line := range strings.Split(rawManifest, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			v := strings.TrimPrefix(line, prefix)
			if v != "" {
				return v
			}
		}
	}
	return "dev"
}
