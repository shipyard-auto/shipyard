// Package runner is the orchestrator that binds every crew subsystem into a
// single execution flow: trigger input in, agent output out.
//
// Run acquires a concurrency slot, resolves the conversation key, loads
// history, reads the prompt, invokes the backend (wiring a per-agent tool
// dispatcher) and persists the updated history. It is the single component
// that sees every other addon package; callers (triggers) treat it as a
// black box.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/backend"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/conversation"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/pool"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

// Input is the payload a trigger hands to the runner for one execution.
// Data is the raw per-trigger map (manual args, cron empty, webhook body).
// Source identifies the trigger ("manual" | "cron" | "webhook:/route").
// TraceID is optional; when empty Runner generates a fresh one.
type Input struct {
	Data    map[string]any
	Source  string
	TraceID string
}

// Output is the result of one Run call. TraceID is always populated, even on
// error, so callers can correlate logs.
type Output struct {
	Text    string
	Usage   backend.Usage
	TraceID string
}

// Emitter is the minimal observer contract the Runner expects from the logs
// subsystem (Task 31). A nil Emitter disables all log emission. The concrete
// implementation lives in addons/crew/internal/crew/logs.
type Emitter interface {
	RunStart(ctx context.Context, agent *crew.Agent, traceID, source string)
	RunEnd(ctx context.Context, agent *crew.Agent, traceID string, out Output, err error)
	ToolCallStart(ctx context.Context, agent *crew.Agent, traceID, tool string, input map[string]any)
	ToolCallEnd(ctx context.Context, agent *crew.Agent, traceID, tool string, env tools.Envelope, err error)
}

// Runner holds the injected dependencies. All fields are required except
// Logs, which may be nil.
type Runner struct {
	Agent      *crew.Agent
	Pool       *pool.Manager
	Store      conversation.Store
	Backend    backend.Backend
	Dispatcher *tools.Dispatcher
	Logs       Emitter
}

// Run executes one agent turn. It always returns an Output with TraceID set,
// and any error already wrapped with the failing step for diagnostics.
func (r *Runner) Run(ctx context.Context, in Input) (Output, error) {
	if r.Agent == nil {
		return Output{}, errors.New("runner: agent is nil")
	}

	traceID := in.TraceID
	if traceID == "" {
		traceID = newTraceID()
	}

	if r.Logs != nil {
		r.Logs.RunStart(ctx, r.Agent, traceID, in.Source)
	}

	out, err := r.runInner(ctx, in, traceID)
	out.TraceID = traceID

	if r.Logs != nil {
		r.Logs.RunEnd(ctx, r.Agent, traceID, out, err)
	}
	return out, err
}

func (r *Runner) runInner(ctx context.Context, in Input, traceID string) (Output, error) {
	poolName := r.Agent.Execution.Pool
	slot, err := r.Pool.Acquire(ctx, poolName)
	if err != nil {
		return Output{}, fmt.Errorf("acquire pool %q: %w", poolName, err)
	}
	defer slot.Release()

	key, err := r.Store.Resolve(r.Agent, in.Data)
	if err != nil {
		return Output{}, fmt.Errorf("resolve key: %w", err)
	}

	history, err := r.Store.Load(ctx, r.Agent, key)
	if err != nil {
		return Output{}, fmt.Errorf("load history: %w", err)
	}

	prompt, err := readPrompt(r.Agent)
	if err != nil {
		return Output{}, fmt.Errorf("read prompt: %w", err)
	}

	user := extractUser(in.Data)

	disp := &agentDispatcher{
		d:       r.Dispatcher,
		agent:   r.Agent,
		traceID: traceID,
		logs:    r.Logs,
	}

	runIn := backend.RunInput{
		Prompt:  prompt,
		User:    user,
		History: history,
		Agent:   r.Agent,
	}
	runOut, err := r.Backend.Run(ctx, runIn, disp)
	if err != nil {
		return Output{}, fmt.Errorf("backend run: %w", err)
	}

	if err := r.Store.Save(ctx, r.Agent, key, runOut.History); err != nil {
		return Output{}, fmt.Errorf("save history: %w", err)
	}

	return Output{Text: runOut.Text, Usage: runOut.Usage}, nil
}

func newTraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func readPrompt(agent *crew.Agent) (string, error) {
	if agent.PromptPath == "" {
		return "", nil
	}
	data, err := os.ReadFile(agent.PromptPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// extractUser derives the user-side string message from the trigger payload.
// Convention: a string value at key "user" wins; otherwise the full payload
// is JSON-marshalled so backends always receive a stable string.
func extractUser(data map[string]any) string {
	if data == nil {
		return ""
	}
	if v, ok := data["user"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	return string(raw)
}

// agentDispatcher is the per-Run adapter that satisfies backend.ToolDispatcher
// by binding the active agent (and traceID) to the shared tools.Dispatcher.
type agentDispatcher struct {
	d       *tools.Dispatcher
	agent   *crew.Agent
	traceID string
	logs    Emitter
}

func (a *agentDispatcher) Call(ctx context.Context, toolName string, input map[string]any) (tools.Envelope, error) {
	if a.logs != nil {
		a.logs.ToolCallStart(ctx, a.agent, a.traceID, toolName, input)
	}
	env, err := a.d.Call(ctx, a.agent, toolName, input)
	if a.logs != nil {
		a.logs.ToolCallEnd(ctx, a.agent, a.traceID, toolName, env, err)
	}
	return env, err
}
