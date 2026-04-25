package logs

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/logs/trace"
)

func TestHandlerEmitsSchemaV2(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	logger := New(SourceCron, Options{Store: store})
	ctx := trace.WithID(context.Background(), "abc123")
	logger.LogAttrs(ctx, slog.LevelInfo, EventCronJobCreated,
		slog.String(KeyEntityID, "AB12CD"),
		slog.String(KeyEntityName, "Backup"),
	)

	files, _ := filepath.Glob(filepath.Join(dir, "cron", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(data))
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal: %v\nline=%s", err, line)
	}

	wants := map[string]string{
		"source":      SourceCron,
		"level":       "INFO",
		"event":       EventCronJobCreated,
		"trace_id":    "abc123",
		"entity_id":   "AB12CD",
		"entity_name": "Backup",
	}
	for k, want := range wants {
		v, ok := got[k]
		if !ok {
			t.Errorf("missing key %q in %v", k, got)
			continue
		}
		if s, _ := v.(string); s != want {
			t.Errorf("%q = %v; want %v", k, v, want)
		}
	}
	for _, mustPresent := range []string{"ts", "hostname", "pid", "service_version"} {
		if _, ok := got[mustPresent]; !ok {
			t.Errorf("missing baseline key %q", mustPresent)
		}
	}
	if _, present := got["msg"]; present {
		t.Errorf("legacy msg key should be renamed to event")
	}
	if _, present := got["time"]; present {
		t.Errorf("legacy time key should be renamed to ts")
	}
}
