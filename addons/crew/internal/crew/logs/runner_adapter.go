package logs

import (
	"context"
	"sync"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/runner"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

// RunnerAdapter satisfies runner.Emitter and forwards events to a logs.Emitter,
// translating the runner's start/end signature into the JSONL event payload.
// It tracks per-call start times in memory to fill the DurationMS field.
//
// A nil RunnerAdapter is safe and behaves as a no-op (matches runner.Emitter
// contract).
type RunnerAdapter struct {
	em  Emitter
	now func() time.Time

	runStarts  sync.Map // map[string]time.Time, key = traceID
	toolStarts sync.Map // map[string]time.Time, key = traceID + "|" + tool
}

// NewRunnerAdapter wraps em so it can be assigned to runner.Runner.Logs.
// Passing a nil em yields nil so callers can short-circuit assignment.
func NewRunnerAdapter(em Emitter) *RunnerAdapter {
	if em == nil {
		return nil
	}
	return &RunnerAdapter{em: em, now: time.Now}
}

// RunStart records the run start time and forwards a run_start event.
func (a *RunnerAdapter) RunStart(_ context.Context, agent *crew.Agent, traceID, source string) {
	if a == nil {
		return
	}
	a.runStarts.Store(traceID, a.now())
	a.em.RunStart(RunStartEvent{
		TraceID: traceID,
		Agent:   agentName(agent),
		Source:  source,
	})
}

// RunEnd computes the duration, emits a run_end event with success/error
// status and, on error, an additional error event so error-only consumers
// catch it without parsing run_end.
func (a *RunnerAdapter) RunEnd(_ context.Context, agent *crew.Agent, traceID string, out runner.Output, err error) {
	if a == nil {
		return
	}
	dur := a.durationMS(&a.runStarts, traceID)
	status := "success"
	var msg string
	if err != nil {
		status = "error"
		msg = err.Error()
	}
	a.em.RunEnd(RunEndEvent{
		TraceID:      traceID,
		Agent:        agentName(agent),
		DurationMS:   dur,
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
		Status:       status,
		ErrorMessage: msg,
	})
	if err != nil {
		a.em.Error(ErrorEvent{
			TraceID: traceID,
			Agent:   agentName(agent),
			Message: msg,
		})
	}
}

// ToolCallStart records the tool start time. The matching ToolCall event is
// emitted by ToolCallEnd so the JSONL stream stays one entry per tool call.
func (a *RunnerAdapter) ToolCallStart(_ context.Context, _ *crew.Agent, traceID, tool string, _ map[string]any) {
	if a == nil {
		return
	}
	a.toolStarts.Store(toolKey(traceID, tool), a.now())
}

// ToolCallEnd emits a tool_call event with computed duration and status.
func (a *RunnerAdapter) ToolCallEnd(_ context.Context, agent *crew.Agent, traceID, tool string, env tools.Envelope, err error) {
	if a == nil {
		return
	}
	dur := a.durationMS(&a.toolStarts, toolKey(traceID, tool))
	ok := err == nil && env.Ok
	var msg string
	switch {
	case err != nil:
		msg = err.Error()
	case !env.Ok:
		msg = env.Error
	}
	a.em.ToolCall(ToolCallEvent{
		TraceID:    traceID,
		Agent:      agentName(agent),
		ToolName:   tool,
		Protocol:   protocolFor(agent, tool),
		DurationMS: dur,
		Ok:         ok,
		Error:      msg,
	})
}

func (a *RunnerAdapter) durationMS(m *sync.Map, key string) int64 {
	v, ok := m.LoadAndDelete(key)
	if !ok {
		return 0
	}
	return a.now().Sub(v.(time.Time)).Milliseconds()
}

func toolKey(traceID, tool string) string {
	return traceID + "|" + tool
}

func agentName(a *crew.Agent) string {
	if a == nil {
		return ""
	}
	return a.Name
}

func protocolFor(a *crew.Agent, tool string) string {
	if a == nil {
		return ""
	}
	for _, t := range a.Tools {
		if t.Name == tool {
			return string(t.Protocol)
		}
	}
	return ""
}
