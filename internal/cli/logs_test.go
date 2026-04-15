package cli

import (
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/internal/logs"
)

func TestLogsLine(t *testing.T) {
	t.Parallel()

	line := logsLine(logs.Event{
		Timestamp: time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC),
		Level:     "info",
		Source:    "cron",
		EntityID:  "AB12CD",
		Message:   "Cron job created",
	})

	if line == "" {
		t.Fatal("logsLine() returned empty string")
	}
}
