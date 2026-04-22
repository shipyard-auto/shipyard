package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/conversation"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

type fakeDisp struct {
	handler func(name string, in map[string]any) (tools.Envelope, error)
	calls   []fakeCall
}

type fakeCall struct {
	name  string
	input map[string]any
}

func (f *fakeDisp) Call(_ context.Context, name string, in map[string]any) (tools.Envelope, error) {
	f.calls = append(f.calls, fakeCall{name: name, input: in})
	if f.handler == nil {
		return tools.Success(nil), nil
	}
	return f.handler(name, in)
}

// scriptedServer serves a pre-programmed sequence of responses. Each call
// pops the next response; optional perRequest allows per-call assertions.
func scriptedServer(t *testing.T, responses []apiResponse, perRequest func(*testing.T, *http.Request, []byte)) *httptest.Server {
	t.Helper()
	idx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if idx >= len(responses) {
			t.Errorf("unexpected extra call #%d", idx+1)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if perRequest != nil {
			perRequest(t, r, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responses[idx])
		idx++
	}))
	t.Cleanup(srv.Close)
	return srv
}

func rawInput(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func withKey(t *testing.T, key string) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", key)
}

func baseAgent(tools ...crew.Tool) *crew.Agent {
	return &crew.Agent{
		Name:    "test",
		Backend: crew.Backend{Type: crew.BackendAnthropicAPI, Model: "claude-sonnet-4-6"},
		Tools:   tools,
	}
}

func TestRun_NoToolUse(t *testing.T) {
	withKey(t, "k")
	srv := scriptedServer(t, []apiResponse{
		{
			Content:    []apiBlock{{Type: "text", Text: "hello world"}},
			StopReason: "end_turn",
			Usage:      apiUsage{InputTokens: 10, OutputTokens: 5},
		},
	}, nil)

	b := NewAPIBackend(WithEndpoint(srv.URL))
	out, err := b.Run(context.Background(), RunInput{
		Prompt: "sys",
		User:   "hi",
		Agent:  baseAgent(),
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text != "hello world" {
		t.Fatalf("Text=%q", out.Text)
	}
	if out.Usage.InputTokens != 10 || out.Usage.OutputTokens != 5 {
		t.Fatalf("Usage=%+v", out.Usage)
	}
	if len(out.History.Messages) != 2 {
		t.Fatalf("History entries=%d want 2", len(out.History.Messages))
	}
	if out.History.Messages[0].Role != "user" || out.History.Messages[1].Role != "assistant" {
		t.Fatalf("roles=%v", out.History.Messages)
	}
}

func TestRun_OneToolUse(t *testing.T) {
	withKey(t, "k")
	srv := scriptedServer(t, []apiResponse{
		{
			Content: []apiBlock{
				{Type: "tool_use", ID: "toolu_1", Name: "foo", Input: rawInput(t, map[string]any{"x": float64(1)})},
			},
			StopReason: "tool_use",
		},
		{
			Content:    []apiBlock{{Type: "text", Text: "done"}},
			StopReason: "end_turn",
		},
	}, nil)

	disp := &fakeDisp{handler: func(name string, in map[string]any) (tools.Envelope, error) {
		return tools.Success(map[string]any{"x": 1}), nil
	}}
	b := NewAPIBackend(WithEndpoint(srv.URL))
	out, err := b.Run(context.Background(), RunInput{
		Prompt: "p",
		User:   "hi",
		Agent:  baseAgent(crew.Tool{Name: "foo", Protocol: crew.ToolExec, Command: []string{"true"}}),
	}, disp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(disp.calls) != 1 {
		t.Fatalf("dispatcher calls=%d want 1", len(disp.calls))
	}
	if disp.calls[0].name != "foo" {
		t.Fatalf("call name=%q", disp.calls[0].name)
	}
	if out.Text != "done" {
		t.Fatalf("Text=%q", out.Text)
	}
	// user + assistant(tool_use) + user(tool_result) + assistant(text) = 4
	if len(out.History.Messages) != 4 {
		t.Fatalf("history len=%d", len(out.History.Messages))
	}
}

func TestRun_TwoSequentialToolCalls(t *testing.T) {
	withKey(t, "k")
	srv := scriptedServer(t, []apiResponse{
		{
			Content:    []apiBlock{{Type: "tool_use", ID: "t1", Name: "foo", Input: rawInput(t, map[string]any{})}},
			StopReason: "tool_use",
		},
		{
			Content:    []apiBlock{{Type: "tool_use", ID: "t2", Name: "foo", Input: rawInput(t, map[string]any{})}},
			StopReason: "tool_use",
		},
		{
			Content:    []apiBlock{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		},
	}, nil)

	disp := &fakeDisp{handler: func(string, map[string]any) (tools.Envelope, error) {
		return tools.Success(nil), nil
	}}
	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(context.Background(), RunInput{
		User:  "x",
		Agent: baseAgent(crew.Tool{Name: "foo", Protocol: crew.ToolExec, Command: []string{"true"}}),
	}, disp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(disp.calls) != 2 {
		t.Fatalf("calls=%d want 2", len(disp.calls))
	}
}

func TestRun_ToolReturnsFailureEnvelope(t *testing.T) {
	withKey(t, "k")
	var gotBody []byte
	srv := scriptedServer(t, []apiResponse{
		{
			Content:    []apiBlock{{Type: "tool_use", ID: "t1", Name: "foo", Input: rawInput(t, map[string]any{})}},
			StopReason: "tool_use",
		},
		{
			Content:    []apiBlock{{Type: "text", Text: "done"}},
			StopReason: "end_turn",
		},
	}, func(_ *testing.T, r *http.Request, body []byte) {
		if r.URL.Path == "/" && len(body) > 0 && gotBody == nil {
			// capture only second request (the one carrying tool_result)
		}
		gotBody = body
	})

	disp := &fakeDisp{handler: func(string, map[string]any) (tools.Envelope, error) {
		return tools.Failure("boom", nil), nil
	}}
	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(context.Background(), RunInput{
		User:  "x",
		Agent: baseAgent(crew.Tool{Name: "foo", Protocol: crew.ToolExec, Command: []string{"true"}}),
	}, disp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// gotBody holds the second request payload — it must contain the
	// tool_result block with is_error:true and an envelope that says "boom".
	if !strings.Contains(string(gotBody), `"is_error":true`) {
		t.Fatalf("expected is_error:true in body, got %s", gotBody)
	}
	if !strings.Contains(string(gotBody), "boom") {
		t.Fatalf("expected envelope error in body, got %s", gotBody)
	}
}

func TestRun_DispatcherReturnsError(t *testing.T) {
	withKey(t, "k")
	var lastBody []byte
	srv := scriptedServer(t, []apiResponse{
		{
			Content:    []apiBlock{{Type: "tool_use", ID: "t1", Name: "nope", Input: rawInput(t, map[string]any{})}},
			StopReason: "tool_use",
		},
		{
			Content:    []apiBlock{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		},
	}, func(_ *testing.T, _ *http.Request, body []byte) {
		lastBody = body
	})

	disp := &fakeDisp{handler: func(string, map[string]any) (tools.Envelope, error) {
		return tools.Envelope{}, errors.New("unknown tool")
	}}
	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(context.Background(), RunInput{
		User:  "x",
		Agent: baseAgent(),
	}, disp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(lastBody), "unknown tool") {
		t.Fatalf("expected error text in tool_result, got %s", lastBody)
	}
	if !strings.Contains(string(lastBody), `"is_error":true`) {
		t.Fatalf("expected is_error:true, got %s", lastBody)
	}
}

func TestRun_MaxIterations(t *testing.T) {
	withKey(t, "k")
	// Always respond with tool_use.
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		resp := apiResponse{
			Content:    []apiBlock{{Type: "tool_use", ID: "x", Name: "foo", Input: rawInput(t, map[string]any{})}},
			StopReason: "tool_use",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	disp := &fakeDisp{handler: func(string, map[string]any) (tools.Envelope, error) {
		return tools.Success(nil), nil
	}}
	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(context.Background(), RunInput{
		User:  "x",
		Agent: baseAgent(crew.Tool{Name: "foo", Protocol: crew.ToolExec, Command: []string{"true"}}),
	}, disp)
	if err == nil || !strings.Contains(err.Error(), "exceeded MaxIterations") {
		t.Fatalf("want MaxIterations error, got %v", err)
	}
	if count != MaxIterations {
		t.Fatalf("expected %d calls, got %d", MaxIterations, count)
	}
}

func TestRun_APIKeyMissing(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	b := NewAPIBackend(WithEndpoint("http://unused"))
	_, err := b.Run(context.Background(), RunInput{User: "hi", Agent: baseAgent()}, nil)
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY not set") {
		t.Fatalf("got err=%v", err)
	}
}

func TestRun_API500(t *testing.T) {
	withKey(t, "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "server blew up", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(context.Background(), RunInput{User: "x", Agent: baseAgent()}, nil)
	if err == nil || !strings.Contains(err.Error(), "anthropic api 500") {
		t.Fatalf("got err=%v", err)
	}
}

func TestRun_APIErrorJSON(t *testing.T) {
	withKey(t, "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"bad"}}`)
	}))
	t.Cleanup(srv.Close)

	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(context.Background(), RunInput{User: "x", Agent: baseAgent()}, nil)
	if err == nil || !strings.Contains(err.Error(), "anthropic error: bad") {
		t.Fatalf("got err=%v", err)
	}
}

func TestRun_ContextCanceledMidLoop(t *testing.T) {
	withKey(t, "k")
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		resp := apiResponse{
			Content:    []apiBlock{{Type: "tool_use", ID: "x", Name: "foo", Input: rawInput(t, map[string]any{})}},
			StopReason: "tool_use",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	disp := &fakeDisp{handler: func(string, map[string]any) (tools.Envelope, error) {
		cancel()
		return tools.Success(nil), nil
	}}
	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(ctx, RunInput{
		User:  "x",
		Agent: baseAgent(crew.Tool{Name: "foo", Protocol: crew.ToolExec, Command: []string{"true"}}),
	}, disp)
	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRun_HistoryReplayed(t *testing.T) {
	withKey(t, "k")
	var gotBody []byte
	srv := scriptedServer(t, []apiResponse{
		{
			Content:    []apiBlock{{Type: "text", Text: "bye"}},
			StopReason: "end_turn",
		},
	}, func(_ *testing.T, _ *http.Request, body []byte) {
		gotBody = body
	})

	prevBlocks := []apiBlock{{Type: "text", Text: "old-user"}}
	prevRaw, _ := json.Marshal(prevBlocks)
	hist := conversation.History{Messages: []conversation.Message{
		{Role: "user", Content: prevRaw},
	}}

	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(context.Background(), RunInput{
		Prompt:  "sys",
		User:    "new-user",
		History: hist,
		Agent:   baseAgent(),
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var req apiRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("want 2 messages in request, got %d (%s)", len(req.Messages), gotBody)
	}
	if req.Messages[0].Content[0].Text != "old-user" {
		t.Fatalf("prev history not replayed: %+v", req.Messages[0])
	}
	if req.Messages[1].Content[0].Text != "new-user" {
		t.Fatalf("new user turn missing: %+v", req.Messages[1])
	}
	if req.System != "sys" {
		t.Fatalf("system prompt=%q", req.System)
	}
}

func TestRun_ToolsConverted(t *testing.T) {
	withKey(t, "k")
	var gotBody []byte
	srv := scriptedServer(t, []apiResponse{
		{
			Content:    []apiBlock{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		},
	}, func(_ *testing.T, _ *http.Request, body []byte) {
		gotBody = body
	})

	agent := &crew.Agent{
		Name:    "t",
		Backend: crew.Backend{Type: crew.BackendAnthropicAPI, Model: "claude-sonnet-4-6"},
		Tools: []crew.Tool{
			{Name: "a", Protocol: crew.ToolExec, Command: []string{"true"}, Description: "alpha", InputSchema: map[string]string{"x": "string"}},
			{Name: "b", Protocol: crew.ToolExec, Command: []string{"true"}, Description: "beta", InputSchema: map[string]string{"n": "number"}},
		},
	}

	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(context.Background(), RunInput{User: "hi", Agent: agent}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var req apiRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(req.Tools))
	}
	names := map[string]bool{req.Tools[0].Name: true, req.Tools[1].Name: true}
	if !names["a"] || !names["b"] {
		t.Fatalf("tool names=%v", names)
	}
	for _, td := range req.Tools {
		var schema map[string]any
		if err := json.Unmarshal(td.InputSchema, &schema); err != nil {
			t.Fatalf("schema decode: %v", err)
		}
		if schema["type"] != "object" {
			t.Fatalf("schema type=%v", schema["type"])
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("properties missing in %v", schema)
		}
		if td.Name == "a" {
			prop, _ := props["x"].(map[string]any)
			if prop["type"] != "string" {
				t.Fatalf("tool a.x type=%v", prop["type"])
			}
		}
		if td.Name == "b" {
			prop, _ := props["n"].(map[string]any)
			if prop["type"] != "number" {
				t.Fatalf("tool b.n type=%v", prop["type"])
			}
		}
	}
}

func TestRun_Headers(t *testing.T) {
	withKey(t, "secret-key")
	var gotKey, gotVer, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewEncoder(w).Encode(apiResponse{
			Content:    []apiBlock{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		})
	}))
	t.Cleanup(srv.Close)

	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(context.Background(), RunInput{User: "x", Agent: baseAgent()}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotKey != "secret-key" {
		t.Fatalf("x-api-key=%q", gotKey)
	}
	if gotVer != anthropicVersion {
		t.Fatalf("anthropic-version=%q", gotVer)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type=%q", gotCT)
	}
}

func TestRun_NilAgent(t *testing.T) {
	withKey(t, "k")
	b := NewAPIBackend(WithEndpoint("http://unused"))
	_, err := b.Run(context.Background(), RunInput{User: "x", Agent: nil}, nil)
	if err == nil || !strings.Contains(err.Error(), "nil agent") {
		t.Fatalf("got err=%v", err)
	}
}

func TestRun_InvalidToolInputJSON(t *testing.T) {
	withKey(t, "k")
	var lastBody []byte
	srv := scriptedServer(t, []apiResponse{
		{
			Content:    []apiBlock{{Type: "tool_use", ID: "t1", Name: "foo", Input: json.RawMessage(`"not an object"`)}},
			StopReason: "tool_use",
		},
		{
			Content:    []apiBlock{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		},
	}, func(_ *testing.T, _ *http.Request, body []byte) {
		lastBody = body
	})

	disp := &fakeDisp{handler: func(string, map[string]any) (tools.Envelope, error) {
		return tools.Success(nil), nil
	}}
	b := NewAPIBackend(WithEndpoint(srv.URL))
	_, err := b.Run(context.Background(), RunInput{User: "x", Agent: baseAgent()}, disp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(lastBody), "invalid tool input") {
		t.Fatalf("expected invalid tool input result, got %s", lastBody)
	}
	if len(disp.calls) != 0 {
		t.Fatalf("dispatcher must not be called on invalid json; got %d", len(disp.calls))
	}
}

// sanity: quick default timeout doesn't block CI
func TestRun_DefaultConstruct(t *testing.T) {
	b := NewAPIBackend()
	if b.endpoint != anthropicEndpoint {
		t.Fatalf("endpoint=%q", b.endpoint)
	}
	if b.httpClient == nil {
		t.Fatal("nil httpClient")
	}
	_ = time.Second
}
