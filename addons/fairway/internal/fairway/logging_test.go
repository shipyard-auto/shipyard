package fairway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

func TestDefaultLogsRootPath(t *testing.T) {
	t.Run("usesShipyardHome", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("SHIPYARD_HOME", root)

		path, err := DefaultLogsRootPath()
		if err != nil {
			t.Fatalf("DefaultLogsRootPath() error = %v", err)
		}
		if want := filepath.Join(root, "logs"); path != want {
			t.Fatalf("path = %q, want %q", path, want)
		}
	})

	t.Run("fallsBackToHome", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("SHIPYARD_HOME", "")
		t.Setenv("HOME", root)

		path, err := DefaultLogsRootPath()
		if err != nil {
			t.Fatalf("DefaultLogsRootPath() error = %v", err)
		}
		if want := filepath.Join(root, ".shipyard", "logs"); path != want {
			t.Fatalf("path = %q, want %q", path, want)
		}
	})
}

func TestNewLoggerWritesFairwayEvents(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIPYARD_HOME", root)

	logger, err := NewLogger()
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	if err := logger.Write(yardlogs.Event{
		Source:     fairwayLogSource,
		Level:      "info",
		Event:      "fairway_test_event",
		Message:    "test",
		EntityType: "daemon",
		EntityID:   "fairway",
		EntityName: "fairway",
	}); err != nil {
		t.Fatalf("logger.Write() error = %v", err)
	}

	logDir := filepath.Join(root, "logs", fairwayLogSource)
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("os.ReadDir(%q) error = %v", logDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}

	data, err := os.ReadFile(filepath.Join(logDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	text := string(data)
	for _, needle := range []string{
		`"source":"fairway"`,
		`"event":"fairway_test_event"`,
		`"entityType":"daemon"`,
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("log file missing %s in %s", needle, text)
		}
	}
}
