package versiondata

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var rawVersion string

func CurrentVersion() string {
	version := strings.TrimSpace(rawVersion)
	if version == "" {
		return "dev"
	}

	return version
}
