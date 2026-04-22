package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

type fakeDriver struct {
	proto crew.ToolProtocol
	last  struct {
		tool  crew.Tool
		input map[string]any
		dc    DriverContext
	}
	ret   Envelope
	err   error
	calls int
}

func (f *fakeDriver) Execute(ctx context.Context, tool crew.Tool, input map[string]any, dc DriverContext) (Envelope, error) {
	f.calls++
	f.last.tool = tool
	f.last.input = input
	f.last.dc = dc
	return f.ret, f.err
}

type countingHook struct {
	startCalls int
	endCalls   int
	lastEnv    Envelope
}

func (h *countingHook) ToolCallStart(ctx context.Context, agent *crew.Agent, tool *crew.Tool, input map[string]any) {
	h.startCalls++
}

func (h *countingHook) ToolCallEnd(ctx context.Context, agent *crew.Agent, tool *crew.Tool, env Envelope, err error) {
	h.endCalls++
	h.lastEnv = env
}

func agentWithTool(name string, tool crew.Tool) *crew.Agent {
	return &crew.Agent{
		Name:  name,
		Dir:   "/tmp/" + name,
		Tools: []crew.Tool{tool},
	}
}

func TestDispatcher_HappyPath(t *testing.T) {
	fake := &fakeDriver{proto: crew.ToolExec, ret: Success(map[string]any{"got": "it"})}
	d := NewDispatcher()
	d.Register(crew.ToolExec, fake)

	agent := agentWithTool("jarvis", crew.Tool{
		Name:     "echo",
		Protocol: crew.ToolExec,
		Command:  []string{"true"},
	})
	input := map[string]any{"a": "b"}
	env, err := d.Call(context.Background(), agent, "echo", input)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !env.Ok {
		t.Fatalf("want ok=true, got %+v", env)
	}
	if fake.calls != 1 {
		t.Fatalf("driver calls = %d", fake.calls)
	}
	if fake.last.input["a"] != "b" {
		t.Fatalf("input propagation broken: %v", fake.last.input)
	}
	if fake.last.dc.AgentName != "jarvis" || fake.last.dc.AgentDir != "/tmp/jarvis" {
		t.Fatalf("driver context not populated: %+v", fake.last.dc)
	}
}

func TestDispatcher_UnknownTool(t *testing.T) {
	d := NewDispatcher()
	agent := &crew.Agent{Name: "j"}
	_, err := d.Call(context.Background(), agent, "nope", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool: nope") {
		t.Fatalf("want unknown-tool error, got %v", err)
	}
}

func TestDispatcher_NoDriverForProtocol(t *testing.T) {
	d := NewDispatcher()
	agent := agentWithTool("j", crew.Tool{
		Name:     "x",
		Protocol: crew.ToolProtocol("xyz"),
	})
	_, err := d.Call(context.Background(), agent, "x", nil)
	if err == nil || !strings.Contains(err.Error(), "no driver registered") {
		t.Fatalf("want no-driver error, got %v", err)
	}
}

func TestDispatcher_ValidationCorrectType(t *testing.T) {
	fake := &fakeDriver{proto: crew.ToolExec, ret: Success(nil)}
	d := NewDispatcher()
	d.Register(crew.ToolExec, fake)

	agent := agentWithTool("j", crew.Tool{
		Name:        "echo",
		Protocol:    crew.ToolExec,
		Command:     []string{"true"},
		InputSchema: map[string]string{"x": "string"},
	})
	_, err := d.Call(context.Background(), agent, "echo", map[string]any{"x": "hello"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("driver calls = %d", fake.calls)
	}
}

func TestDispatcher_ValidationWrongType(t *testing.T) {
	fake := &fakeDriver{proto: crew.ToolExec, ret: Success(nil)}
	d := NewDispatcher()
	d.Register(crew.ToolExec, fake)

	agent := agentWithTool("j", crew.Tool{
		Name:        "echo",
		Protocol:    crew.ToolExec,
		Command:     []string{"true"},
		InputSchema: map[string]string{"x": "string"},
	})
	_, err := d.Call(context.Background(), agent, "echo", map[string]any{"x": 123})
	if err == nil || !strings.Contains(err.Error(), "input validation") {
		t.Fatalf("want input-validation error, got %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("driver must not be called on validation failure, got %d calls", fake.calls)
	}
}

func TestDispatcher_ValidationNumberAcceptsIntAndFloat(t *testing.T) {
	fake := &fakeDriver{proto: crew.ToolExec, ret: Success(nil)}
	d := NewDispatcher()
	d.Register(crew.ToolExec, fake)

	agent := agentWithTool("j", crew.Tool{
		Name:        "echo",
		Protocol:    crew.ToolExec,
		Command:     []string{"true"},
		InputSchema: map[string]string{"x": "number"},
	})

	if _, err := d.Call(context.Background(), agent, "echo", map[string]any{"x": 42}); err != nil {
		t.Fatalf("int rejected: %v", err)
	}
	if _, err := d.Call(context.Background(), agent, "echo", map[string]any{"x": 3.14}); err != nil {
		t.Fatalf("float64 rejected: %v", err)
	}
	if _, err := d.Call(context.Background(), agent, "echo", map[string]any{"x": int64(7)}); err != nil {
		t.Fatalf("int64 rejected: %v", err)
	}
	if _, err := d.Call(context.Background(), agent, "echo", map[string]any{"x": "nope"}); err == nil {
		t.Fatalf("string must not satisfy number schema")
	}
}

func TestDispatcher_ValidationAllTypes(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		good    any
		bad     any
		skipBad bool
	}{
		{name: "boolean", schema: "boolean", good: true, bad: "true"},
		{name: "object", schema: "object", good: map[string]any{"k": 1}, bad: []any{}},
		{name: "array", schema: "array", good: []any{1, 2}, bad: map[string]any{}},
		{name: "unknown-liberal", schema: "zzz", good: "anything", bad: nil, skipBad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDriver{proto: crew.ToolExec, ret: Success(nil)}
			d := NewDispatcher()
			d.Register(crew.ToolExec, fake)
			agent := agentWithTool("j", crew.Tool{
				Name:        "echo",
				Protocol:    crew.ToolExec,
				Command:     []string{"true"},
				InputSchema: map[string]string{"x": tc.schema},
			})
			if _, err := d.Call(context.Background(), agent, "echo", map[string]any{"x": tc.good}); err != nil {
				t.Fatalf("good value rejected: %v", err)
			}
			if tc.skipBad {
				return
			}
			if _, err := d.Call(context.Background(), agent, "echo", map[string]any{"x": tc.bad}); err == nil {
				t.Fatalf("bad value accepted")
			}
		})
	}
}

func TestDispatcher_ValidationMissingFieldIsOK(t *testing.T) {
	fake := &fakeDriver{proto: crew.ToolExec, ret: Success(nil)}
	d := NewDispatcher()
	d.Register(crew.ToolExec, fake)

	agent := agentWithTool("j", crew.Tool{
		Name:        "echo",
		Protocol:    crew.ToolExec,
		Command:     []string{"true"},
		InputSchema: map[string]string{"x": "string", "y": "number"},
	})
	if _, err := d.Call(context.Background(), agent, "echo", map[string]any{"x": "a"}); err != nil {
		t.Fatalf("missing y rejected: %v", err)
	}
}

func TestDispatcher_ValidationExtraFieldIsOK(t *testing.T) {
	fake := &fakeDriver{proto: crew.ToolExec, ret: Success(nil)}
	d := NewDispatcher()
	d.Register(crew.ToolExec, fake)

	agent := agentWithTool("j", crew.Tool{
		Name:        "echo",
		Protocol:    crew.ToolExec,
		Command:     []string{"true"},
		InputSchema: map[string]string{"x": "string"},
	})
	if _, err := d.Call(context.Background(), agent, "echo", map[string]any{"x": "a", "z": "extra"}); err != nil {
		t.Fatalf("extra field rejected: %v", err)
	}
}

func TestDispatcher_RegisterOverridesDefault(t *testing.T) {
	fake := &fakeDriver{proto: crew.ToolExec, ret: Success(map[string]any{"from": "fake"})}
	d := NewDispatcher()
	d.Register(crew.ToolExec, fake)

	agent := agentWithTool("j", crew.Tool{
		Name:     "echo",
		Protocol: crew.ToolExec,
		Command:  []string{"true"},
	})
	env, err := d.Call(context.Background(), agent, "echo", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !env.Ok || !strings.Contains(string(env.Data), "fake") {
		t.Fatalf("override not honored: %+v", env)
	}
	if fake.calls != 1 {
		t.Fatalf("fake calls = %d", fake.calls)
	}
}

func TestDispatcher_LogHookInvoked(t *testing.T) {
	fake := &fakeDriver{proto: crew.ToolExec, ret: Success(nil)}
	hook := &countingHook{}
	d := NewDispatcher()
	d.Register(crew.ToolExec, fake)
	d.SetLogHook(hook)

	agent := agentWithTool("j", crew.Tool{
		Name:     "echo",
		Protocol: crew.ToolExec,
		Command:  []string{"true"},
	})
	if _, err := d.Call(context.Background(), agent, "echo", nil); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if hook.startCalls != 1 || hook.endCalls != 1 {
		t.Fatalf("hook calls: start=%d end=%d", hook.startCalls, hook.endCalls)
	}
}

func TestDispatcher_LogHookNilIsSafe(t *testing.T) {
	fake := &fakeDriver{proto: crew.ToolExec, ret: Success(nil)}
	d := NewDispatcher()
	d.Register(crew.ToolExec, fake)

	agent := agentWithTool("j", crew.Tool{
		Name:     "echo",
		Protocol: crew.ToolExec,
		Command:  []string{"true"},
	})
	if _, err := d.Call(context.Background(), agent, "echo", nil); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDispatcher_NewDispatcherHasDefaultDrivers(t *testing.T) {
	d := NewDispatcher()
	if _, ok := d.drivers[crew.ToolExec]; !ok {
		t.Errorf("missing default exec driver")
	}
	if _, ok := d.drivers[crew.ToolHTTP]; !ok {
		t.Errorf("missing default http driver")
	}
}

func TestDispatcher_NilAgent(t *testing.T) {
	d := NewDispatcher()
	_, err := d.Call(context.Background(), nil, "x", nil)
	if err == nil {
		t.Fatalf("want error for nil agent")
	}
}
