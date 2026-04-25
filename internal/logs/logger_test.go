package logs

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func TestLogger_NoSampler_KeepsAllRecords(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	logger := New(SourceCron, Options{Store: store})
	for i := 0; i < 5; i++ {
		logger.LogAttrs(context.Background(), slog.LevelInfo, EventCronJobCreated)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "cron", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	lines := readLines(t, files[0])
	if len(lines) != 5 {
		t.Errorf("got %d lines, want 5 (no sampler should keep everything)", len(lines))
	}
}

func TestLogger_SamplerCanDropRecords(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	keepEven := SamplerFunc(func(_ context.Context, r slog.Record) bool {
		var i int64
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "i" {
				i = a.Value.Int64()
			}
			return true
		})
		return i%2 == 0
	})

	logger := New(SourceCron, Options{Store: store, Sampler: keepEven})
	for i := int64(0); i < 6; i++ {
		logger.LogAttrs(context.Background(), slog.LevelInfo, EventCronJobCreated, slog.Int64("i", i))
	}

	files, _ := filepath.Glob(filepath.Join(dir, "cron", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	lines := readLines(t, files[0])
	if len(lines) != 3 {
		t.Errorf("sampler kept %d lines, want 3 (i=0,2,4)", len(lines))
	}
}
