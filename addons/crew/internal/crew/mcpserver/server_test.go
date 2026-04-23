package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

// fakeHandler lets tests fabricate a tool catalogue and a Call function
// without bringing up the real dispatcher.
type fakeHandler struct {
	tools []crew.Tool
	call  func(ctx context.Context, name string, args map[string]any) (tools.Envelope, error)
}

func (h *fakeHandler) Tools() []crew.Tool { return h.tools }
func (h *fakeHandler) Call(ctx context.Context, name string, args map[string]any) (tools.Envelope, error) {
	return h.call(ctx, name, args)
}

// runFrames feeds frames through the server and returns each response as a
// parsed map. Requests with no id (notifications) should produce no frame —
// this helper blocks until EOF is consumed and returns exactly what was
// written.
func runFrames(t *testing.T, s *Server, frames ...string) []map[string]any {
	t.Helper()
	in := strings.Join(frames, "\n") + "\n"
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	return parseLines(t, out.String())
}

func parseLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	if strings.TrimSpace(s) == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func newTestServer(h Handler) *Server {
	return NewServer(h, "shipyard-crew-test", "0.0.0")
}

func TestServer_Handshake(t *testing.T) {
	s := newTestServer(&fakeHandler{tools: nil})
	out := runFrames(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	)
	if len(out) != 1 {
		t.Fatalf("want 1 response (initialize), got %d: %v", len(out), out)
	}
	resp := out[0]
	if resp["jsonrpc"] != "2.0" {
		t.Fatalf("jsonrpc: %v", resp["jsonrpc"])
	}
	if id, _ := resp["id"].(float64); id != 1 {
		t.Fatalf("id: %v", resp["id"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result: %v", resp["result"])
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion: %v", result["protocolVersion"])
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok || info["name"] != "shipyard-crew-test" {
		t.Fatalf("serverInfo: %v", result["serverInfo"])
	}
}

func TestServer_ToolsList(t *testing.T) {
	h := &fakeHandler{
		tools: []crew.Tool{
			{Name: "echo", Protocol: crew.ToolExec, Description: "echo",
				InputSchema: map[string]string{"text": "string"}},
			{Name: "ping", Protocol: crew.ToolExec, Description: "ping"},
		},
	}
	s := newTestServer(h)
	out := runFrames(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)
	if len(out) != 1 {
		t.Fatalf("want 1 response, got %d", len(out))
	}
	result := out[0]["result"].(map[string]any)
	toolsArr, ok := result["tools"].([]any)
	if !ok || len(toolsArr) != 2 {
		t.Fatalf("tools: %v", result["tools"])
	}
	first := toolsArr[0].(map[string]any)
	if first["name"] != "echo" {
		t.Fatalf("first tool: %v", first)
	}
	if _, ok := first["inputSchema"].(map[string]any); !ok {
		t.Fatalf("inputSchema missing: %v", first)
	}
}

func TestServer_ToolsCall_Success(t *testing.T) {
	h := &fakeHandler{
		tools: []crew.Tool{{Name: "echo", Protocol: crew.ToolExec}},
		call: func(ctx context.Context, name string, args map[string]any) (tools.Envelope, error) {
			if name != "echo" {
				t.Fatalf("unexpected tool: %s", name)
			}
			if args["text"] != "hi" {
				t.Fatalf("unexpected args: %v", args)
			}
			return tools.Success(map[string]any{"echoed": "hi"}), nil
		},
	}
	s := newTestServer(h)
	out := runFrames(t, s, `{"jsonrpc":"2.0","id":"a","method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	result := out[0]["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("should not be error: %v", result)
	}
	sc, ok := result["structuredContent"].(map[string]any)
	if !ok || sc["echoed"] != "hi" {
		t.Fatalf("structuredContent: %v", result["structuredContent"])
	}
}

func TestServer_ToolsCall_HandlerError(t *testing.T) {
	h := &fakeHandler{
		call: func(ctx context.Context, name string, args map[string]any) (tools.Envelope, error) {
			return tools.Envelope{}, errors.New("unknown tool: ghost")
		},
	}
	s := newTestServer(h)
	out := runFrames(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ghost"}}`)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	result := out[0]["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("should be error")
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "unknown tool") {
		t.Fatalf("unexpected error text: %q", text)
	}
}

func TestServer_ToolsCall_EnvelopeFailure(t *testing.T) {
	h := &fakeHandler{
		call: func(ctx context.Context, name string, args map[string]any) (tools.Envelope, error) {
			return tools.Failure("driver exited 2", nil), nil
		},
	}
	s := newTestServer(h)
	out := runFrames(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whatever"}}`)
	result := out[0]["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("should be error")
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "driver exited 2") {
		t.Fatalf("text: %q", text)
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	s := newTestServer(&fakeHandler{})
	out := runFrames(t, s, `{"jsonrpc":"2.0","id":9,"method":"nope"}`)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	errBlock, ok := out[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error block: %v", out[0])
	}
	if code, _ := errBlock["code"].(float64); int(code) != ErrMethodNotFound {
		t.Fatalf("code: %v", errBlock["code"])
	}
}

func TestServer_InvalidParams(t *testing.T) {
	s := newTestServer(&fakeHandler{})
	out := runFrames(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"oops"}`)
	errBlock, ok := out[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error: %v", out[0])
	}
	if code, _ := errBlock["code"].(float64); int(code) != ErrInvalidParams {
		t.Fatalf("code: %v", errBlock["code"])
	}
}

func TestServer_ParseError(t *testing.T) {
	s := newTestServer(&fakeHandler{})
	out := runFrames(t, s, `{not json`)
	errBlock, ok := out[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error: %v", out[0])
	}
	if code, _ := errBlock["code"].(float64); int(code) != ErrParse {
		t.Fatalf("code: %v", errBlock["code"])
	}
}

func TestServer_NotificationProducesNoResponse(t *testing.T) {
	s := newTestServer(&fakeHandler{})
	out := runFrames(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(out) != 0 {
		t.Fatalf("notification must not produce response, got %v", out)
	}
}

func TestServer_EOFIsCleanExit(t *testing.T) {
	s := newTestServer(&fakeHandler{})
	r := strings.NewReader("")
	var w bytes.Buffer
	if err := s.Serve(context.Background(), r, &w); err != nil {
		t.Fatalf("eof should be clean, got %v", err)
	}
	if w.Len() != 0 {
		t.Fatalf("unexpected output: %q", w.String())
	}
}

func TestServer_WriteMutexSerialisesConcurrentWrites(t *testing.T) {
	// Not a server-level behaviour test per se, but verifies the Writer
	// used inside Serve is safe under heavy concurrent fabricated calls —
	// which matters if we ever parallelise dispatch.
	var buf bytes.Buffer
	w := NewWriter(&buf)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			_ = w.Write(JSONRPCResponse{JSONRPC: "2.0", ID: json.RawMessage([]byte("1")), Result: map[string]int{"i": i}})
		}()
	}
	wg.Wait()
	if strings.Count(buf.String(), "\n") != 200 {
		t.Fatalf("want 200 lines, got %d", strings.Count(buf.String(), "\n"))
	}
}

// Regression guard: Next() should return a copy, so callers can hold two
// frames simultaneously without one clobbering the other.
func TestReader_FramesAreCopies(t *testing.T) {
	in := `{"a":1}` + "\n" + `{"b":2}` + "\n"
	r := NewReader(strings.NewReader(in))
	first, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != `{"a":1}` {
		t.Fatalf("first got clobbered: %q", first)
	}
	if string(second) != `{"b":2}` {
		t.Fatalf("second: %q", second)
	}
}

// Smoke: ensure io.Discard path does not panic.
var _ io.Writer = io.Discard
