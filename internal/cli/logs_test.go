package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/internal/logs"
)

func TestLogFilterFlags_toFilter_appliesSince(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	f := logFilterFlags{since: 90 * time.Minute, sources: []string{"cron"}, entityID: " ab12cd ", traceID: " t-1 "}
	got := f.toFilter(now)
	if !got.Since.Equal(now.Add(-90 * time.Minute)) {
		t.Errorf("Since = %v, want %v", got.Since, now.Add(-90*time.Minute))
	}
	if got.EntityID != "AB12CD" {
		t.Errorf("EntityID = %q, want AB12CD (uppercased + trimmed)", got.EntityID)
	}
	if got.TraceID != "t-1" {
		t.Errorf("TraceID = %q, want t-1 (trimmed)", got.TraceID)
	}
	if len(got.Sources) != 1 || got.Sources[0] != "cron" {
		t.Errorf("Sources = %v, want [cron]", got.Sources)
	}
}

func TestLogFilterFlags_toFilter_zeroSinceLeavesUnset(t *testing.T) {
	t.Parallel()
	got := logFilterFlags{}.toFilter(time.Now())
	if !got.Since.IsZero() {
		t.Errorf("Since must stay zero when --since is not set; got %v", got.Since)
	}
}

func TestPrettyTailWriter_rendersJSONLLines(t *testing.T) {
	t.Parallel()
	out := &strings.Builder{}
	pw := &prettyTailWriter{out: out, render: logs.RenderOptions{ShowSource: true}}

	line := `{"ts":"2026-04-24T10:00:00Z","level":"info","source":"cron","event":"cron_job_created","entity_id":"AB12CD","entity_name":"Backup","message":"created"}` + "\n"
	if _, err := pw.Write([]byte(line)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "AB12CD") {
		t.Errorf("expected entity id in pretty output; got %q", got)
	}
	if !strings.Contains(got, "cron") {
		t.Errorf("expected source name in pretty output; got %q", got)
	}
}

func TestPrettyTailWriter_skipsMalformedLines(t *testing.T) {
	t.Parallel()
	out := &strings.Builder{}
	pw := &prettyTailWriter{out: out}
	if _, err := pw.Write([]byte("not-json\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if out.String() != "" {
		t.Errorf("malformed JSONL should produce no pretty output; got %q", out.String())
	}
}
