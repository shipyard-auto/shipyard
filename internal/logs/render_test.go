package logs

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestRenderPretty_showTrace_fullID guards against the regression where the
// trace id was truncated to 8 chars in pretty output. Truncation broke
// "copy the trace, grep the JSONL" workflows because the printed id did not
// match what the JSONL stored. The full id is short enough to fit on a line.
func TestRenderPretty_showTrace_fullID(t *testing.T) {
	const full = "deadbeefcafebabe" // canonical 16-char hex trace id
	rec := Record{
		Timestamp: time.Now(),
		Level:     "INFO",
		Source:    SourceCron,
		Event:     "cron_job_run_finished",
		TraceID:   full,
		EntityID:  "AB12CD",
	}
	var buf bytes.Buffer
	if err := RenderPretty(&buf, rec, RenderOptions{ShowTrace: true}); err != nil {
		t.Fatalf("RenderPretty: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "trace="+full) {
		t.Fatalf("pretty output missing full trace id %q; got: %s", full, out)
	}
	// Defensive: assert no 8-char-truncated form leaks through.
	if strings.Contains(out, "trace="+full[:8]+" ") || strings.HasSuffix(strings.TrimSpace(out), "trace="+full[:8]) {
		t.Fatalf("pretty output appears to still truncate trace id; got: %s", out)
	}
}

// TestRenderPretty_showTrace_disabled_omitsTrace confirms the trace token is
// only added when callers opt-in. ShowTrace=false should yield no "trace="
// substring at all.
func TestRenderPretty_showTrace_disabled_omitsTrace(t *testing.T) {
	rec := Record{
		Timestamp: time.Now(),
		Level:     "INFO",
		Source:    SourceCron,
		Event:     "cron_job_run_finished",
		TraceID:   "deadbeefcafebabe",
		EntityID:  "AB12CD",
	}
	var buf bytes.Buffer
	if err := RenderPretty(&buf, rec, RenderOptions{ShowTrace: false}); err != nil {
		t.Fatalf("RenderPretty: %v", err)
	}
	if strings.Contains(buf.String(), "trace=") {
		t.Fatalf("ShowTrace=false should omit trace; got: %s", buf.String())
	}
}
