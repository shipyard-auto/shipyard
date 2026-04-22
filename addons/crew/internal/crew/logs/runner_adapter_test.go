package logs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/backend"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/runner"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

type recEmitter struct {
	mu     sync.Mutex
	starts []RunStartEvent
	ends   []RunEndEvent
	tools  []ToolCallEvent
	errs   []ErrorEvent
	closed atomic.Bool
}

func (r *recEmitter) RunStart(e RunStartEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, e)
}
func (r *recEmitter) RunEnd(e RunEndEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ends = append(r.ends, e)
}
func (r *recEmitter) ToolCall(e ToolCallEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = append(r.tools, e)
}
func (r *recEmitter) Error(e ErrorEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, e)
}
func (r *recEmitter) Close() error { r.closed.Store(true); return nil }

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

func TestRunnerAdapter_RunSuccess_EmitsStartAndEnd(t *testing.T) {
	rec := &recEmitter{}
	ad := NewRunnerAdapter(rec)
	ad.now = stepClock(time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC), 50*time.Millisecond)

	ag := sampleAgent()
	ctx := context.Background()
	ad.RunStart(ctx, ag, "trace-1", "manual")
	ad.RunEnd(ctx, ag, "trace-1",
		runner.Output{TraceID: "trace-1", Text: "hi", Usage: backend.Usage{InputTokens: 3, OutputTokens: 7}},
		nil,
	)

	if len(rec.starts) != 1 || rec.starts[0].TraceID != "trace-1" || rec.starts[0].Source != "manual" || rec.starts[0].Agent != "alpha" {
		t.Fatalf("RunStart not propagated correctly: %+v", rec.starts)
	}
	if len(rec.ends) != 1 {
		t.Fatalf("want 1 RunEnd got %d", len(rec.ends))
	}
	end := rec.ends[0]
	if end.Status != "success" {
		t.Errorf("Status=%q, want success", end.Status)
	}
	if end.InputTokens != 3 || end.OutputTokens != 7 {
		t.Errorf("usage not propagated: %+v", end)
	}
	if end.DurationMS != 50 {
		t.Errorf("DurationMS=%d, want 50", end.DurationMS)
	}
	if len(rec.errs) != 0 {
		t.Errorf("no Error events expected on success, got %d", len(rec.errs))
	}
}

func TestRunnerAdapter_RunError_EmitsRunEndAndErrorEvent(t *testing.T) {
	rec := &recEmitter{}
	ad := NewRunnerAdapter(rec)
	ad.now = stepClock(time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC), 100*time.Millisecond)

	ag := sampleAgent()
	ctx := context.Background()
	ad.RunStart(ctx, ag, "trace-2", "cron")
	ad.RunEnd(ctx, ag, "trace-2", runner.Output{TraceID: "trace-2"}, errors.New("boom"))

	if len(rec.ends) != 1 || rec.ends[0].Status != "error" || rec.ends[0].ErrorMessage != "boom" {
		t.Fatalf("RunEnd error not propagated: %+v", rec.ends)
	}
	if len(rec.errs) != 1 || rec.errs[0].Message != "boom" || rec.errs[0].TraceID != "trace-2" {
		t.Fatalf("Error event missing or wrong: %+v", rec.errs)
	}
}

func TestRunnerAdapter_ToolCall_EmitsOnEndOnly(t *testing.T) {
	rec := &recEmitter{}
	ad := NewRunnerAdapter(rec)
	ad.now = stepClock(time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC), 25*time.Millisecond)

	ag := sampleAgent()
	ctx := context.Background()
	ad.ToolCallStart(ctx, ag, "trace-3", "fetch", map[string]any{"url": "x"})
	ad.ToolCallEnd(ctx, ag, "trace-3", "fetch", tools.Success(map[string]any{"ok": 1}), nil)

	if len(rec.tools) != 1 {
		t.Fatalf("want 1 ToolCall got %d", len(rec.tools))
	}
	tc := rec.tools[0]
	if tc.ToolName != "fetch" || tc.Protocol != "http" {
		t.Errorf("tool/protocol wrong: %+v", tc)
	}
	if !tc.Ok || tc.Error != "" {
		t.Errorf("expected ok=true and no error: %+v", tc)
	}
	if tc.DurationMS != 25 {
		t.Errorf("DurationMS=%d, want 25", tc.DurationMS)
	}
}

func TestRunnerAdapter_ToolCallFailure_PropagatesError(t *testing.T) {
	rec := &recEmitter{}
	ad := NewRunnerAdapter(rec)
	ag := sampleAgent()
	ctx := context.Background()

	// Driver-reported failure (envelope.Ok=false).
	ad.ToolCallStart(ctx, ag, "t", "fetch", nil)
	ad.ToolCallEnd(ctx, ag, "t", "fetch", tools.Failure("upstream 500", nil), nil)

	// Transport error (err != nil).
	ad.ToolCallStart(ctx, ag, "t", "ls", nil)
	ad.ToolCallEnd(ctx, ag, "t", "ls", tools.Envelope{}, errors.New("exec failed"))

	if len(rec.tools) != 2 {
		t.Fatalf("want 2 ToolCall got %d", len(rec.tools))
	}
	if rec.tools[0].Ok || rec.tools[0].Error != "upstream 500" {
		t.Errorf("envelope failure not surfaced: %+v", rec.tools[0])
	}
	if rec.tools[1].Ok || rec.tools[1].Error != "exec failed" {
		t.Errorf("transport failure not surfaced: %+v", rec.tools[1])
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
	rec := &recEmitter{}
	ad := NewRunnerAdapter(rec)
	ad.ToolCallStart(context.Background(), sampleAgent(), "t", "unknown", nil)
	ad.ToolCallEnd(context.Background(), sampleAgent(), "t", "unknown", tools.Envelope{Ok: true}, nil)
	if rec.tools[0].Protocol != "" {
		t.Errorf("Protocol for unknown tool: want empty, got %q", rec.tools[0].Protocol)
	}
}
