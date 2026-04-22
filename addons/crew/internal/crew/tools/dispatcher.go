package tools

import (
	"context"
	"fmt"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// LogHook is an optional observer plugged into the dispatcher so that the
// logs emitter (Task 31) can record tool invocations without coupling this
// package to any logging implementation. Both methods are best-effort and
// must not panic or block meaningfully — the dispatcher does not synchronize
// them with the driver call.
type LogHook interface {
	ToolCallStart(ctx context.Context, agent *crew.Agent, tool *crew.Tool, input map[string]any)
	ToolCallEnd(ctx context.Context, agent *crew.Agent, tool *crew.Tool, env Envelope, err error)
}

// Dispatcher routes a named tool invocation to the correct protocol driver.
// It holds its own driver registry (no package-level globals) and validates
// the input against the tool's declared InputSchema before dispatching.
type Dispatcher struct {
	drivers map[crew.ToolProtocol]Driver
	logs    LogHook
}

// NewDispatcher returns a dispatcher pre-registered with the exec and http
// drivers. Callers can override either via Register.
func NewDispatcher() *Dispatcher {
	d := &Dispatcher{drivers: map[crew.ToolProtocol]Driver{}}
	d.Register(crew.ToolExec, NewExecDriver())
	d.Register(crew.ToolHTTP, NewHTTPDriver())
	return d
}

// Register installs drv as the driver for protocol p, replacing any previous
// registration. Intended for test injection and for potential future
// protocols; production wiring happens in NewDispatcher.
func (d *Dispatcher) Register(p crew.ToolProtocol, drv Driver) {
	d.drivers[p] = drv
}

// SetLogHook attaches (or clears, if h is nil) the optional observer invoked
// around every Call.
func (d *Dispatcher) SetLogHook(h LogHook) { d.logs = h }

// Call resolves toolName within agent.Tools, validates input against the
// tool's InputSchema, selects the driver registered for the tool's protocol
// and delegates execution to it. Contract violations (unknown tool, missing
// driver, input type mismatch) return a non-nil error and never invoke the
// driver; runtime failures surface as Envelope{Ok:false}.
func (d *Dispatcher) Call(ctx context.Context, agent *crew.Agent, toolName string, input map[string]any) (Envelope, error) {
	if agent == nil {
		return Envelope{}, fmt.Errorf("dispatcher: nil agent")
	}

	var tool *crew.Tool
	for i := range agent.Tools {
		if agent.Tools[i].Name == toolName {
			tool = &agent.Tools[i]
			break
		}
	}
	if tool == nil {
		return Envelope{}, fmt.Errorf("unknown tool: %s", toolName)
	}

	for field, expected := range tool.InputSchema {
		value, present := input[field]
		if !present {
			continue
		}
		if !validateType(expected, value) {
			return Envelope{}, fmt.Errorf("input validation: field %q expected %s, got %T", field, expected, value)
		}
	}

	drv, ok := d.drivers[tool.Protocol]
	if !ok {
		return Envelope{}, fmt.Errorf("no driver registered for protocol %q", tool.Protocol)
	}

	if d.logs != nil {
		d.logs.ToolCallStart(ctx, agent, tool, input)
	}

	dc := DriverContext{
		AgentName: agent.Name,
		AgentDir:  agent.Dir,
	}
	env, err := drv.Execute(ctx, *tool, input, dc)

	if d.logs != nil {
		d.logs.ToolCallEnd(ctx, agent, tool, env, err)
	}

	return env, err
}

// validateType reports whether v matches the structural type expected by the
// schema. Unknown type strings are treated liberally (accept); the Task 05
// loader rejects invalid schema types at agent load time, so this path is
// belt-and-suspenders for tests and future extensions.
func validateType(expected string, v any) bool {
	switch expected {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		switch v.(type) {
		case float64, float32, int, int64, int32:
			return true
		}
		return false
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	default:
		return true
	}
}
