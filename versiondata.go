package versiondata

import (
	_ "embed"
	"strings"
)

//go:embed manifest
var rawManifest string

func CurrentVersion() string {
	for _, line := range strings.Split(rawManifest, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "shipyard=") {
			v := strings.TrimPrefix(line, "shipyard=")
			if v != "" {
				return v
			}
		}
	}
	return "dev"
}
