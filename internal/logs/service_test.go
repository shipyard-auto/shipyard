package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteAndQuery(t *testing.T) {
	t.Parallel()

	rootDir := filepath.Join(t.TempDir(), "logs")
	svc := Service{
		ConfigStore: ConfigStore{Path: filepath.Join(t.TempDir(), "logs.json")},
		RootDir:     rootDir,
		Now:         func() time.Time { return time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC) },
	}

	err := svc.Write(Event{
		Source:     DefaultSourceCron,
		Level:      "info",
		Event:      "cron_job_created",
		Message:    "Cron job created",
		EntityType: "cron_job",
		EntityID:   "AB12CD",
		EntityName: "Backup",
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	events, err := svc.Query(Query{Source: DefaultSourceCron, Entity: "AB12CD", Limit: 10})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Event != "cron_job_created" {
		t.Fatalf("events[0].Event = %q, want %q", events[0].Event, "cron_job_created")
	}
}

func TestPruneDeletesOldFiles(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	root := filepath.Join(tmp, "logs")
	configPath := filepath.Join(tmp, "logs.json")
	svc := Service{
		ConfigStore: ConfigStore{Path: configPath},
		RootDir:     root,
		Now:         func() time.Time { return time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC) },
	}
	if err := svc.ConfigStore.Save(Config{RetentionDays: 14}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	oldPath := dailyLogPath(root, DefaultSourceCron, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(oldPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, err := svc.Prune()
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if result.DeletedFiles != 1 {
		t.Fatalf("DeletedFiles = %d, want 1", result.DeletedFiles)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old log file still exists")
	}
}

func TestListSources(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	root := filepath.Join(tmp, "logs")
	path := dailyLogPath(root, DefaultSourceCron, time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	svc := Service{ConfigStore: ConfigStore{Path: filepath.Join(tmp, "logs.json")}, RootDir: root, Now: time.Now}
	sources, err := svc.ListSources()
	if err != nil {
		t.Fatalf("ListSources() error = %v", err)
	}
	if len(sources) != 1 || sources[0].Source != DefaultSourceCron {
		t.Fatalf("sources = %#v, want one cron source", sources)
	}
}

func TestFormatEvent(t *testing.T) {
	t.Parallel()

	line := formatEvent(Event{
		Timestamp: time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC),
		Level:     "info",
		Source:    DefaultSourceCron,
		EntityID:  "AB12CD",
		Message:   "Cron job created",
	})
	if !strings.Contains(line, "AB12CD") || !strings.Contains(line, "Cron job created") {
		t.Fatalf("formatEvent() = %q", line)
	}
}
