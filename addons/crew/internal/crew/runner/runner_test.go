package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/backend"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/config"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/conversation"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/pool"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

// --- fakes -----------------------------------------------------------------

type fakeStore struct {
	resolveKey string
	resolveErr error
	loadHist   conversation.History
	loadErr    error
	saveErr    error

	resolveCalls int
	loadCalls    int
	saveCalls    int
	savedKey     string
	savedHist    conversation.History
}

func (f *fakeStore) Resolve(a *crew.Agent, in map[string]any) (string, error) {
	f.resolveCalls++
	return f.resolveKey, f.resolveErr
}
func (f *fakeStore) Load(ctx context.Context, a *crew.Agent, k string) (conversation.History, error) {
	f.loadCalls++
	return f.loadHist, f.loadErr
}
func (f *fakeStore) Save(ctx context.Context, a *crew.Agent, k string, h conversation.History) error {
	f.saveCalls++
	f.savedKey = k
	f.savedHist = h
	return f.saveErr
}

type fakeBackend struct {
	out backend.RunOutput
	err error

	got      backend.RunInput
	gotDisp  backend.ToolDispatcher
	callHook func(ctx context.Context, disp backend.ToolDispatcher)
}

func (f *fakeBackend) Run(ctx context.Context, in backend.RunInput, d backend.ToolDispatcher) (backend.RunOutput, error) {
	f.got = in
	f.gotDisp = d
	if f.callHook != nil {
		f.callHook(ctx, d)
	}
	return f.out, f.err
}

type fakeEmitter struct {
	runStart atomic.Int32
	runEnd   atomic.Int32
	toolStr  atomic.Int32
	toolEnd  atomic.Int32

	lastRunStartSource string
	lastRunEndOut      Output
	lastRunEndErr      error
	lastToolName       string
	lastToolTrace      string
}

func (e *fakeEmitter) RunStart(ctx context.Context, a *crew.Agent, traceID, source string) {
	e.runStart.Add(1)
	e.lastRunStartSource = source
}
func (e *fakeEmitter) RunEnd(ctx context.Context, a *crew.Agent, traceID string, out Output, err error) {
	e.runEnd.Add(1)
	e.lastRunEndOut = out
	e.lastRunEndErr = err
}
func (e *fakeEmitter) ToolCallStart(ctx context.Context, a *crew.Agent, traceID, tool string, input map[string]any) {
	e.toolStr.Add(1)
	e.lastToolName = tool
	e.lastToolTrace = traceID
}
func (e *fakeEmitter) ToolCallEnd(ctx context.Context, a *crew.Agent, traceID, tool string, env tools.Envelope, err error) {
	e.toolEnd.Add(1)
}

// --- helpers ---------------------------------------------------------------

func testAgent(t *testing.T, promptPath string) *crew.Agent {
	t.Helper()
	return &crew.Agent{
		Name: "sample",
		Backend: crew.Backend{
			Type:    crew.BackendCLI,
			Command: []string{"echo"},
		},
		Execution:    crew.Execution{Mode: crew.ExecutionOnDemand, Pool: "cli"},
		Conversation: crew.Conversation{Mode: crew.ConversationStateless},
		PromptPath:   promptPath,
	}
}

func testPool(max int, strategy config.QueueStrategy) *pool.Manager {
	return pool.NewManager(&config.ConcurrencyConfig{
		DefaultPool: "cli",
		Pools:       map[string]config.PoolConfig{"cli": {Max: max}},
		Queue: config.QueueConfig{
			Strategy:     strategy,
			MaxWait:      100 * time.Millisecond,
			MaxQueueSize: 4,
		},
	})
}

// --- tests -----------------------------------------------------------------

func TestRunHappyPath(t *testing.T) {
	store := &fakeStore{
		resolveKey: "k",
		loadHist:   conversation.History{},
	}
	savedHist := conversation.History{SessionID: "session-42"}
	be := &fakeBackend{
		out: backend.RunOutput{
			Text:    "ok",
			History: savedHist,
			Usage:   backend.Usage{InputTokens: 3, OutputTokens: 4},
		},
	}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(2, config.QueueWait),
		Store:      store,
		Backend:    be,
		Dispatcher: tools.NewDispatcher(),
	}

	out, err := r.Run(context.Background(), Input{Source: "manual"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text != "ok" {
		t.Fatalf("Text=%q", out.Text)
	}
	if out.TraceID == "" {
		t.Fatal("TraceID empty")
	}
	if out.Usage.InputTokens != 3 || out.Usage.OutputTokens != 4 {
		t.Fatalf("Usage=%+v", out.Usage)
	}
	if store.savedKey != "k" || store.savedHist.SessionID != "session-42" {
		t.Fatalf("save args: key=%q hist=%+v", store.savedKey, store.savedHist)
	}
	if store.resolveCalls != 1 || store.loadCalls != 1 || store.saveCalls != 1 {
		t.Fatalf("store calls: resolve=%d load=%d save=%d",
			store.resolveCalls, store.loadCalls, store.saveCalls)
	}
}

func TestRunTraceIDPropagated(t *testing.T) {
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    &fakeBackend{},
		Dispatcher: tools.NewDispatcher(),
	}
	out, err := r.Run(context.Background(), Input{TraceID: "fixed-123"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.TraceID != "fixed-123" {
		t.Fatalf("TraceID=%q", out.TraceID)
	}
}

func TestRunTraceIDGenerated(t *testing.T) {
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    &fakeBackend{},
		Dispatcher: tools.NewDispatcher(),
	}
	out, err := r.Run(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.TraceID) != 16 {
		t.Fatalf("TraceID len=%d: %q", len(out.TraceID), out.TraceID)
	}
	for _, c := range out.TraceID {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex char in TraceID: %q", out.TraceID)
		}
	}
}

func TestRunPoolFullRejects(t *testing.T) {
	p := testPool(1, config.QueueReject)
	// Hold the only slot.
	holder, err := p.Acquire(context.Background(), "cli")
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	defer holder.Release()

	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       p,
		Store:      &fakeStore{},
		Backend:    &fakeBackend{},
		Dispatcher: tools.NewDispatcher(),
	}
	_, err = r.Run(context.Background(), Input{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "acquire pool") {
		t.Fatalf("want error with 'acquire pool' prefix, got %v", err)
	}
	if !errors.Is(err, pool.ErrPoolFull) {
		t.Fatalf("want ErrPoolFull in chain, got %v", err)
	}
}

func TestRunResolveError(t *testing.T) {
	store := &fakeStore{resolveErr: errors.New("boom")}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      store,
		Backend:    &fakeBackend{},
		Dispatcher: tools.NewDispatcher(),
	}
	_, err := r.Run(context.Background(), Input{})
	if err == nil || !strings.HasPrefix(err.Error(), "resolve key:") {
		t.Fatalf("want resolve key prefix, got %v", err)
	}
	if store.loadCalls != 0 || store.saveCalls != 0 {
		t.Fatalf("downstream should not be called")
	}
}

func TestRunLoadError(t *testing.T) {
	store := &fakeStore{loadErr: errors.New("disk")}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      store,
		Backend:    &fakeBackend{},
		Dispatcher: tools.NewDispatcher(),
	}
	_, err := r.Run(context.Background(), Input{})
	if err == nil || !strings.HasPrefix(err.Error(), "load history:") {
		t.Fatalf("want load history prefix, got %v", err)
	}
}

func TestRunBackendError(t *testing.T) {
	store := &fakeStore{}
	be := &fakeBackend{err: errors.New("api down")}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      store,
		Backend:    be,
		Dispatcher: tools.NewDispatcher(),
	}
	_, err := r.Run(context.Background(), Input{})
	if err == nil || !strings.HasPrefix(err.Error(), "backend run:") {
		t.Fatalf("want backend run prefix, got %v", err)
	}
	if store.saveCalls != 0 {
		t.Fatal("Save must not run after backend error")
	}
}

func TestRunSaveError(t *testing.T) {
	store := &fakeStore{saveErr: errors.New("disk full")}
	be := &fakeBackend{out: backend.RunOutput{Text: "ok"}}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      store,
		Backend:    be,
		Dispatcher: tools.NewDispatcher(),
	}
	_, err := r.Run(context.Background(), Input{})
	if err == nil || !strings.HasPrefix(err.Error(), "save history:") {
		t.Fatalf("want save history prefix, got %v", err)
	}
}

func TestRunPromptReadFromDisk(t *testing.T) {
	dir := t.TempDir()
	prompt := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(prompt, []byte("SYSTEM PROMPT"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	be := &fakeBackend{}
	r := &Runner{
		Agent:      testAgent(t, prompt),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    be,
		Dispatcher: tools.NewDispatcher(),
	}
	if _, err := r.Run(context.Background(), Input{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if be.got.Prompt != "SYSTEM PROMPT" {
		t.Fatalf("prompt=%q", be.got.Prompt)
	}
}

func TestRunPromptMissingPathIsEmpty(t *testing.T) {
	be := &fakeBackend{}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    be,
		Dispatcher: tools.NewDispatcher(),
	}
	if _, err := r.Run(context.Background(), Input{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if be.got.Prompt != "" {
		t.Fatalf("prompt=%q", be.got.Prompt)
	}
}

func TestRunPromptReadError(t *testing.T) {
	be := &fakeBackend{}
	r := &Runner{
		Agent:      testAgent(t, "/nonexistent/definitely/missing/prompt.md"),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    be,
		Dispatcher: tools.NewDispatcher(),
	}
	_, err := r.Run(context.Background(), Input{})
	if err == nil || !strings.HasPrefix(err.Error(), "read prompt:") {
		t.Fatalf("want read prompt prefix, got %v", err)
	}
}

func TestRunUserFromDataUserKey(t *testing.T) {
	be := &fakeBackend{}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    be,
		Dispatcher: tools.NewDispatcher(),
	}
	if _, err := r.Run(context.Background(), Input{Data: map[string]any{"user": "hi"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if be.got.User != "hi" {
		t.Fatalf("User=%q", be.got.User)
	}
}

func TestRunUserFallbackJSON(t *testing.T) {
	be := &fakeBackend{}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    be,
		Dispatcher: tools.NewDispatcher(),
	}
	if _, err := r.Run(context.Background(), Input{Data: map[string]any{"chat_id": 5}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(be.got.User), &parsed); err != nil {
		t.Fatalf("User not valid JSON: %q err=%v", be.got.User, err)
	}
	if _, ok := parsed["chat_id"]; !ok {
		t.Fatalf("chat_id missing from JSON: %q", be.got.User)
	}
}

func TestRunUserNilData(t *testing.T) {
	be := &fakeBackend{}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    be,
		Dispatcher: tools.NewDispatcher(),
	}
	if _, err := r.Run(context.Background(), Input{Data: nil}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if be.got.User != "" {
		t.Fatalf("User=%q", be.got.User)
	}
}

func TestRunDispatcherAdapter(t *testing.T) {
	agent := testAgent(t, "")
	agent.Tools = []crew.Tool{{
		Name:     "echo_tool",
		Protocol: crew.ToolExec,
		Command:  []string{"true"},
	}}

	disp := tools.NewDispatcher()
	// Replace exec driver with a fake to avoid spawning processes.
	disp.Register(crew.ToolExec, driverFunc(func(ctx context.Context, tl crew.Tool, in map[string]any, dc tools.DriverContext) (tools.Envelope, error) {
		if tl.Name != "echo_tool" {
			t.Errorf("driver got tool %q", tl.Name)
		}
		if dc.AgentName != "sample" {
			t.Errorf("driver got agent %q", dc.AgentName)
		}
		return tools.Success(map[string]string{"saw": "ok"}), nil
	}))

	var dispatchErr error
	var gotEnv tools.Envelope
	be := &fakeBackend{callHook: func(ctx context.Context, d backend.ToolDispatcher) {
		gotEnv, dispatchErr = d.Call(ctx, "echo_tool", map[string]any{})
	}}

	r := &Runner{
		Agent:      agent,
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    be,
		Dispatcher: disp,
	}
	if _, err := r.Run(context.Background(), Input{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if dispatchErr != nil {
		t.Fatalf("dispatch err: %v", dispatchErr)
	}
	if !gotEnv.Ok {
		t.Fatalf("env not ok: %+v", gotEnv)
	}
}

// driverFunc adapts a plain function to tools.Driver for tests.
type driverFunc func(ctx context.Context, tl crew.Tool, in map[string]any, dc tools.DriverContext) (tools.Envelope, error)

func (f driverFunc) Execute(ctx context.Context, tl crew.Tool, in map[string]any, dc tools.DriverContext) (tools.Envelope, error) {
	return f(ctx, tl, in, dc)
}

func TestRunEmitterNilSafe(t *testing.T) {
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    &fakeBackend{},
		Dispatcher: tools.NewDispatcher(),
		Logs:       nil,
	}
	if _, err := r.Run(context.Background(), Input{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunEmitterReceivesRunStartEnd(t *testing.T) {
	em := &fakeEmitter{}
	be := &fakeBackend{out: backend.RunOutput{Text: "ok"}}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    be,
		Dispatcher: tools.NewDispatcher(),
		Logs:       em,
	}
	if _, err := r.Run(context.Background(), Input{Source: "manual"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if em.runStart.Load() != 1 {
		t.Fatalf("RunStart=%d", em.runStart.Load())
	}
	if em.runEnd.Load() != 1 {
		t.Fatalf("RunEnd=%d", em.runEnd.Load())
	}
	if em.lastRunStartSource != "manual" {
		t.Fatalf("source=%q", em.lastRunStartSource)
	}
	if em.lastRunEndOut.Text != "ok" || em.lastRunEndOut.TraceID == "" {
		t.Fatalf("RunEnd out=%+v", em.lastRunEndOut)
	}
	if em.lastRunEndErr != nil {
		t.Fatalf("RunEnd err=%v", em.lastRunEndErr)
	}
}

func TestRunEmitterReceivesToolCalls(t *testing.T) {
	agent := testAgent(t, "")
	agent.Tools = []crew.Tool{{
		Name:     "my_tool",
		Protocol: crew.ToolExec,
		Command:  []string{"true"},
	}}

	disp := tools.NewDispatcher()
	disp.Register(crew.ToolExec, driverFunc(func(ctx context.Context, tl crew.Tool, in map[string]any, dc tools.DriverContext) (tools.Envelope, error) {
		return tools.Success(nil), nil
	}))

	em := &fakeEmitter{}
	be := &fakeBackend{callHook: func(ctx context.Context, d backend.ToolDispatcher) {
		_, _ = d.Call(ctx, "my_tool", map[string]any{})
	}}

	r := &Runner{
		Agent:      agent,
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    be,
		Dispatcher: disp,
		Logs:       em,
	}
	out, err := r.Run(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if em.toolStr.Load() != 1 || em.toolEnd.Load() != 1 {
		t.Fatalf("tool events: start=%d end=%d", em.toolStr.Load(), em.toolEnd.Load())
	}
	if em.lastToolName != "my_tool" {
		t.Fatalf("tool name=%q", em.lastToolName)
	}
	if em.lastToolTrace != out.TraceID {
		t.Fatalf("trace mismatch: tool=%q run=%q", em.lastToolTrace, out.TraceID)
	}
}

func TestRunCtxCancelBeforeAcquire(t *testing.T) {
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      &fakeStore{},
		Backend:    &fakeBackend{},
		Dispatcher: tools.NewDispatcher(),
	}
	// Hold the only slot so Acquire has to wait, then cancel immediately.
	holder, err := r.Pool.Acquire(context.Background(), "cli")
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	defer holder.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = r.Run(ctx, Input{})
	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestRunNilAgent(t *testing.T) {
	r := &Runner{}
	_, err := r.Run(context.Background(), Input{})
	if err == nil || !strings.Contains(err.Error(), "agent is nil") {
		t.Fatalf("want agent is nil, got %v", err)
	}
}

func TestOutputTraceIDSetOnError(t *testing.T) {
	store := &fakeStore{resolveErr: errors.New("boom")}
	r := &Runner{
		Agent:      testAgent(t, ""),
		Pool:       testPool(1, config.QueueWait),
		Store:      store,
		Backend:    &fakeBackend{},
		Dispatcher: tools.NewDispatcher(),
	}
	out, err := r.Run(context.Background(), Input{TraceID: "abc"})
	if err == nil {
		t.Fatal("expected error")
	}
	if out.TraceID != "abc" {
		t.Fatalf("TraceID not propagated on error: %q", out.TraceID)
	}
}
