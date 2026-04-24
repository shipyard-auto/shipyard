package logs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/backend"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/runner"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/logs/trace"
)

// recordedEntry pairs a slog.Record with the trace id derived from the ctx
// it was emitted under. The production Handler injects trace as an attr at
// write time; the test handler does the same so assertions can inspect it.
type recordedEntry struct {
	record  slog.Record
	traceID string
}

type recordingHandler struct {
	mu      sync.Mutex
	entries []recordedEntry
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *recordingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, recordedEntry{record: r.Clone(), traceID: trace.ID(ctx)})
	return nil
}
func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler     { return h }

func (h *recordingHandler) snapshot() []recordedEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]recordedEntry, len(h.entries))
	copy(out, h.entries)
	return out
}

// findAttr returns the value of the first attr matching key, or nil.
func findAttr(r slog.Record, key string) (slog.Value, bool) {
	var (
		val   slog.Value
		found bool
	)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val = a.Value
			found = true
			return false
		}
		return true
	})
	return val, found
}

func newAdapter(t *testing.T, step time.Duration) (*RunnerAdapter, *recordingHandler) {
	t.Helper()
	rec := &recordingHandler{}
	ad := NewRunnerAdapter(slog.New(rec))
	ad.now = stepClock(time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC), step)
	return ad, rec
}

func sampleAgent() *crew.Agent {
	return &crew.Agent{
		Name: "alpha",
		Tools: []crew.Tool{
			{Name: "fetch", Protocol: crew.ToolHTTP},
			{Name: "ls", Protocol: crew.ToolExec},
		},
	}
}

// stepClock returns a function that advances by step every call.
func stepClock(start time.Time, step time.Duration) func() time.Time {
	cur := start
	return func() time.Time {
		out := cur
		cur = cur.Add(step)
		return out
	}
}

func eventsByName(entries []recordedEntry) map[string][]recordedEntry {
	out := make(map[string][]recordedEntry)
	for _, e := range entries {
		out[e.record.Message] = append(out[e.record.Message], e)
	}
	return out
}

func TestRunnerAdapter_RunSuccess_EmitsStartAndEnd(t *testing.T) {
	ad, rec := newAdapter(t, 50*time.Millisecond)

	ag := sampleAgent()
	ctx := context.Background()
	ad.RunStart(ctx, ag, "trace-1", "manual")
	ad.RunEnd(ctx, ag, "trace-1",
		runner.Output{TraceID: "trace-1", Text: "hi", Usage: backend.Usage{InputTokens: 3, OutputTokens: 7}},
		nil,
	)

	byName := eventsByName(rec.snapshot())

	starts := byName[yardlogs.EventRunStart]
	if len(starts) != 1 {
		t.Fatalf("RunStart: got %d records, want 1", len(starts))
	}
	if starts[0].traceID != "trace-1" {
		t.Errorf("RunStart trace_id = %q, want trace-1", starts[0].traceID)
	}
	if v, ok := findAttr(starts[0].record, "agent"); !ok || v.String() != "alpha" {
		t.Errorf("RunStart agent attr = %v (ok=%v), want alpha", v, ok)
	}
	if v, ok := findAttr(starts[0].record, "crew_source"); !ok || v.String() != "manual" {
		t.Errorf("RunStart crew_source = %v, want manual", v)
	}

	ends := byName[yardlogs.EventRunEnd]
	if len(ends) != 1 {
		t.Fatalf("RunEnd: got %d records, want 1", len(ends))
	}
	endRec := ends[0].record
	if v, ok := findAttr(endRec, "status"); !ok || v.String() != "success" {
		t.Errorf("RunEnd status = %v, want success", v)
	}
	if v, ok := findAttr(endRec, yardlogs.KeyDurationMs); !ok || v.Int64() != 50 {
		t.Errorf("RunEnd duration_ms = %v, want 50", v)
	}
	if v, ok := findAttr(endRec, yardlogs.KeyTokensInput); !ok || v.Int64() != 3 {
		t.Errorf("RunEnd tokens_input = %v, want 3", v)
	}
	if v, ok := findAttr(endRec, yardlogs.KeyTokensOutput); !ok || v.Int64() != 7 {
		t.Errorf("RunEnd tokens_output = %v, want 7", v)
	}
	if errs := byName[yardlogs.EventRunError]; len(errs) != 0 {
		t.Errorf("no run_error expected on success, got %d", len(errs))
	}
}

func TestRunnerAdapter_RunError_EmitsRunEndAndErrorEvent(t *testing.T) {
	ad, rec := newAdapter(t, 100*time.Millisecond)

	ag := sampleAgent()
	ctx := context.Background()
	ad.RunStart(ctx, ag, "trace-2", "cron")
	ad.RunEnd(ctx, ag, "trace-2", runner.Output{TraceID: "trace-2"}, errors.New("boom"))

	byName := eventsByName(rec.snapshot())
	ends := byName[yardlogs.EventRunEnd]
	if len(ends) != 1 {
		t.Fatalf("RunEnd: got %d records, want 1", len(ends))
	}
	if v, ok := findAttr(ends[0].record, "status"); !ok || v.String() != "error" {
		t.Errorf("status = %v, want error", v)
	}
	if v, ok := findAttr(ends[0].record, yardlogs.KeyError); !ok || v.String() != "boom" {
		t.Errorf("error = %v, want boom", v)
	}

	errEvents := byName[yardlogs.EventRunError]
	if len(errEvents) != 1 {
		t.Fatalf("run_error: got %d records, want 1", len(errEvents))
	}
	if errEvents[0].traceID != "trace-2" {
		t.Errorf("run_error trace_id = %q, want trace-2", errEvents[0].traceID)
	}
}

func TestRunnerAdapter_ToolCall_EmitsStartAndEnd(t *testing.T) {
	ad, rec := newAdapter(t, 25*time.Millisecond)

	ag := sampleAgent()
	ctx := context.Background()
	ad.ToolCallStart(ctx, ag, "trace-3", "fetch", map[string]any{"url": "x"})
	ad.ToolCallEnd(ctx, ag, "trace-3", "fetch", tools.Success(map[string]any{"ok": 1}), nil)

	byName := eventsByName(rec.snapshot())
	if got := len(byName[yardlogs.EventToolCallStart]); got != 1 {
		t.Fatalf("tool_call_start: got %d, want 1", got)
	}
	ends := byName[yardlogs.EventToolCallEnd]
	if len(ends) != 1 {
		t.Fatalf("tool_call_end: got %d, want 1", len(ends))
	}
	endRec := ends[0].record
	if v, ok := findAttr(endRec, yardlogs.KeyToolName); !ok || v.String() != "fetch" {
		t.Errorf("tool_name = %v, want fetch", v)
	}
	if v, ok := findAttr(endRec, yardlogs.KeyToolProtocol); !ok || v.String() != "http" {
		t.Errorf("tool_protocol = %v, want http", v)
	}
	if v, ok := findAttr(endRec, yardlogs.KeyToolOK); !ok || !v.Bool() {
		t.Errorf("tool_ok = %v, want true", v)
	}
	if v, ok := findAttr(endRec, yardlogs.KeyDurationMs); !ok || v.Int64() != 25 {
		t.Errorf("duration_ms = %v, want 25", v)
	}
}

func TestRunnerAdapter_ToolCallFailure_PropagatesError(t *testing.T) {
	ad, rec := newAdapter(t, 25*time.Millisecond)
	ag := sampleAgent()
	ctx := context.Background()

	// Driver-reported failure (envelope.Ok=false).
	ad.ToolCallStart(ctx, ag, "t", "fetch", nil)
	ad.ToolCallEnd(ctx, ag, "t", "fetch", tools.Failure("upstream 500", nil), nil)

	// Transport error (err != nil).
	ad.ToolCallStart(ctx, ag, "t", "ls", nil)
	ad.ToolCallEnd(ctx, ag, "t", "ls", tools.Envelope{}, errors.New("exec failed"))

	ends := eventsByName(rec.snapshot())[yardlogs.EventToolCallEnd]
	if len(ends) != 2 {
		t.Fatalf("tool_call_end: got %d, want 2", len(ends))
	}

	if v, ok := findAttr(ends[0].record, yardlogs.KeyToolOK); !ok || v.Bool() {
		t.Errorf("[0] tool_ok = %v, want false", v)
	}
	if v, ok := findAttr(ends[0].record, yardlogs.KeyError); !ok || v.String() != "upstream 500" {
		t.Errorf("[0] error = %v, want upstream 500", v)
	}

	if v, ok := findAttr(ends[1].record, yardlogs.KeyToolOK); !ok || v.Bool() {
		t.Errorf("[1] tool_ok = %v, want false", v)
	}
	if v, ok := findAttr(ends[1].record, yardlogs.KeyError); !ok || v.String() != "exec failed" {
		t.Errorf("[1] error = %v, want exec failed", v)
	}
}

func TestRunnerAdapter_NilSafe(t *testing.T) {
	if NewRunnerAdapter(nil) != nil {
		t.Fatalf("NewRunnerAdapter(nil) must return nil")
	}
	var ad *RunnerAdapter
	// All methods must tolerate a nil receiver without panicking.
	ad.RunStart(context.Background(), nil, "t", "manual")
	ad.RunEnd(context.Background(), nil, "t", runner.Output{}, nil)
	ad.ToolCallStart(context.Background(), nil, "t", "x", nil)
	ad.ToolCallEnd(context.Background(), nil, "t", "x", tools.Envelope{Ok: true}, nil)
}

func TestRunnerAdapter_ProtocolFallbackEmpty(t *testing.T) {
	ad, rec := newAdapter(t, 1*time.Millisecond)
	ad.ToolCallStart(context.Background(), sampleAgent(), "t", "unknown", nil)
	ad.ToolCallEnd(context.Background(), sampleAgent(), "t", "unknown", tools.Envelope{Ok: true}, nil)

	ends := eventsByName(rec.snapshot())[yardlogs.EventToolCallEnd]
	if len(ends) != 1 {
		t.Fatalf("tool_call_end: got %d, want 1", len(ends))
	}
	if v, ok := findAttr(ends[0].record, yardlogs.KeyToolProtocol); !ok || v.String() != "" {
		t.Errorf("tool_protocol = %v, want empty", v)
	}
}

// TestRunnerAdapter_TraceIDPropagatesViaContext asserts that when a trace
// id is present on the parent ctx it is preserved end-to-end and not
// overridden by the trailing string argument.
func TestRunnerAdapter_TraceIDPropagatesViaContext(t *testing.T) {
	ad, rec := newAdapter(t, 1*time.Millisecond)
	ctx := trace.WithID(context.Background(), "ctx-trace")
	ad.RunStart(ctx, sampleAgent(), "arg-trace", "manual")
	entries := rec.snapshot()
	if len(entries) != 1 {
		t.Fatalf("got %d records, want 1", len(entries))
	}
	if entries[0].traceID != "ctx-trace" {
		t.Errorf("trace propagation: got %q, want ctx-trace", entries[0].traceID)
	}
}
