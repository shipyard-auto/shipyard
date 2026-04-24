// Package logs adapts the crew runner's Emitter contract to the unified
// shipyard slog logging pipeline. Each method translates a runner event
// into a structured slog record with the schema-v2 attribute set.
package logs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/runner"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/logs/trace"
)

// RunnerAdapter satisfies runner.Emitter and translates each event into a
// structured slog record. Per-call start times are tracked in memory so
// the corresponding end events carry an accurate duration_ms.
//
// A nil RunnerAdapter is safe and behaves as a no-op (matches runner.Emitter
// contract).
type RunnerAdapter struct {
	logger *slog.Logger
	now    func() time.Time

	runStarts  sync.Map // key = traceID, value = time.Time
	toolStarts sync.Map // key = traceID + "|" + tool, value = time.Time
}

// NewRunnerAdapter wraps logger so it can be assigned to runner.Runner.Logs.
// Passing a nil logger yields nil so callers can short-circuit assignment
// and avoid all per-event work.
func NewRunnerAdapter(logger *slog.Logger) *RunnerAdapter {
	if logger == nil {
		return nil
	}
	return &RunnerAdapter{logger: logger, now: time.Now}
}

// RunStart records the run start time and emits a run_start record.
func (a *RunnerAdapter) RunStart(ctx context.Context, agent *crew.Agent, traceID, source string) {
	if a == nil {
		return
	}
	a.runStarts.Store(traceID, a.now())
	a.logger.LogAttrs(a.ctxWithTrace(ctx, traceID), slog.LevelInfo, yardlogs.EventRunStart,
		a.agentAttrs(agent,
			slog.String("agent", agentName(agent)),
			slog.String("crew_source", source),
		)...,
	)
}

// RunEnd computes the elapsed duration, emits a run_end record carrying
// the final status and token usage, and on error emits a separate
// run_error record so error-only consumers do not need to parse run_end.
func (a *RunnerAdapter) RunEnd(ctx context.Context, agent *crew.Agent, traceID string, out runner.Output, err error) {
	if a == nil {
		return
	}
	dur := a.durationMS(&a.runStarts, traceID)
	level := slog.LevelInfo
	status := "success"
	var msg string
	if err != nil {
		level = slog.LevelError
		status = "error"
		msg = err.Error()
	}

	endAttrs := a.agentAttrs(agent,
		slog.String("agent", agentName(agent)),
		slog.Int64(yardlogs.KeyDurationMs, dur),
		slog.Int(yardlogs.KeyTokensInput, out.Usage.InputTokens),
		slog.Int(yardlogs.KeyTokensOutput, out.Usage.OutputTokens),
		slog.String("status", status),
	)
	if msg != "" {
		endAttrs = append(endAttrs,
			slog.String(yardlogs.KeyError, msg),
			slog.String(yardlogs.KeyErrorKind, fmt.Sprintf("%T", err)),
		)
	}
	tracedCtx := a.ctxWithTrace(ctx, traceID)
	a.logger.LogAttrs(tracedCtx, level, yardlogs.EventRunEnd, endAttrs...)

	if err != nil {
		a.logger.LogAttrs(tracedCtx, slog.LevelError, yardlogs.EventRunError,
			a.agentAttrs(agent,
				slog.String("agent", agentName(agent)),
				slog.String(yardlogs.KeyError, msg),
				slog.String(yardlogs.KeyErrorKind, fmt.Sprintf("%T", err)),
			)...,
		)
	}
}

// ToolCallStart records the tool start time and emits a tool_call_start
// record so consumers can correlate long-running tool invocations with
// their eventual end.
func (a *RunnerAdapter) ToolCallStart(ctx context.Context, agent *crew.Agent, traceID, tool string, _ map[string]any) {
	if a == nil {
		return
	}
	a.toolStarts.Store(toolKey(traceID, tool), a.now())
	a.logger.LogAttrs(a.ctxWithTrace(ctx, traceID), slog.LevelInfo, yardlogs.EventToolCallStart,
		a.agentAttrs(agent,
			slog.String("agent", agentName(agent)),
			slog.String(yardlogs.KeyToolName, tool),
			slog.String(yardlogs.KeyToolProtocol, protocolFor(agent, tool)),
		)...,
	)
}

// ToolCallEnd emits a tool_call_end record with computed duration and
// the per-tool ok/error status.
func (a *RunnerAdapter) ToolCallEnd(ctx context.Context, agent *crew.Agent, traceID, tool string, env tools.Envelope, err error) {
	if a == nil {
		return
	}
	dur := a.durationMS(&a.toolStarts, toolKey(traceID, tool))
	ok := err == nil && env.Ok
	level := slog.LevelInfo
	if !ok {
		level = slog.LevelError
	}

	var msg string
	switch {
	case err != nil:
		msg = err.Error()
	case !env.Ok:
		msg = env.Error
	}

	attrs := a.agentAttrs(agent,
		slog.String("agent", agentName(agent)),
		slog.String(yardlogs.KeyToolName, tool),
		slog.String(yardlogs.KeyToolProtocol, protocolFor(agent, tool)),
		slog.Int64(yardlogs.KeyDurationMs, dur),
		slog.Bool(yardlogs.KeyToolOK, ok),
	)
	if msg != "" {
		attrs = append(attrs, slog.String(yardlogs.KeyError, msg))
	}
	if err != nil {
		attrs = append(attrs, slog.String(yardlogs.KeyErrorKind, fmt.Sprintf("%T", err)))
	}

	a.logger.LogAttrs(a.ctxWithTrace(ctx, traceID), level, yardlogs.EventToolCallEnd, attrs...)
}

// ctxWithTrace ensures the slog Handler can lift a trace id from ctx even
// when the caller provided one only via the Emitter argument. It does not
// override an inbound trace id already present on ctx.
func (a *RunnerAdapter) ctxWithTrace(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if trace.ID(ctx) != "" || traceID == "" {
		return ctx
	}
	return trace.WithID(ctx, traceID)
}

// agentAttrs returns the canonical agent entity attrs followed by any
// extras. The entity_* keys keep Loki/Grafana labels stable across cron,
// service and crew sources.
func (a *RunnerAdapter) agentAttrs(ag *crew.Agent, extras ...slog.Attr) []slog.Attr {
	attrs := make([]slog.Attr, 0, 3+len(extras))
	attrs = append(attrs, yardlogs.EntityAttrs(yardlogs.EntityAgent, agentName(ag), agentName(ag))...)
	attrs = append(attrs, extras...)
	return attrs
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
