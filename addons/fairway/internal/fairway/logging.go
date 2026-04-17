package fairway

import (
	"os"
	"path/filepath"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

const fairwayLogSource = "fairway"

// EventLogger is the minimal logging boundary used by the Fairway daemon.
type EventLogger interface {
	Write(event yardlogs.Event) error
}

// NewLogger constructs the default Fairway JSONL event logger.
func NewLogger() (EventLogger, error) {
	rootDir, err := DefaultLogsRootPath()
	if err != nil {
		return nil, err
	}
	return yardlogs.Service{
		RootDir: rootDir,
		Now:     time.Now,
	}, nil
}

// DefaultLogsRootPath returns the log root used by Fairway events.
func DefaultLogsRootPath() (string, error) {
	if shipyardHome := os.Getenv("SHIPYARD_HOME"); shipyardHome != "" {
		return filepath.Join(shipyardHome, "logs"), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".shipyard", "logs"), nil
}

type noopLogger struct{}

func (noopLogger) Write(yardlogs.Event) error { return nil }
