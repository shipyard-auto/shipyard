package logs

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/shipyard-auto/shipyard/internal/metadata"
)

func DefaultPaths() (configPath, rootDir string, err error) {
	shipyardHome, err := metadata.DefaultHomeDir()
	if err != nil {
		return "", "", err
	}

	return filepath.Join(shipyardHome, "logs.json"), filepath.Join(shipyardHome, "logs"), nil
}

func sourceDir(rootDir, source string) string {
	return filepath.Join(rootDir, source)
}

func dailyLogPath(rootDir, source string, at time.Time) string {
	return filepath.Join(sourceDir(rootDir, source), fmt.Sprintf("%s.jsonl", at.UTC().Format("2006-01-02")))
}
