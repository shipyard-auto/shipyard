package logs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/logs/trace"
)

func TestMiddlewareEmitsRequestRecord(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()
	logger := New(SourceFairway, Options{Store: store})

	var seenTrace string
	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTrace = trace.ID(r.Context())
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hi"))
	}))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/x", strings.NewReader("body"))
	req.Header.Set(HeaderTraceID, "fixed-trace")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if seenTrace != "fixed-trace" {
		t.Fatalf("seenTrace = %q; want fixed-trace", seenTrace)
	}
	if got := resp.Header.Get(HeaderTraceID); got != "fixed-trace" {
		t.Fatalf("response trace = %q; want fixed-trace", got)
	}

	files, _ := filepath.Glob(filepath.Join(dir, SourceFairway, "*.jsonl"))
	if len(files) == 0 {
		t.Fatal("no log file written")
	}
	data, _ := os.ReadFile(files[0])
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["http_status"].(float64) != 418 {
		t.Errorf("http_status = %v; want 418", got["http_status"])
	}
	if got["http_method"] != "POST" {
		t.Errorf("http_method = %v; want POST", got["http_method"])
	}
	if got["trace_id"] != "fixed-trace" {
		t.Errorf("trace_id = %v; want fixed-trace", got["trace_id"])
	}
}

// TestMiddleware_markResponseBody_emitsOutputTail asserts that a handler
// can opt in to logging the response body via MarkResponseBody and that
// the middleware then attaches output_tail (last DefaultOutputTailBytes)
// to the http_request record.
func TestMiddleware_markResponseBody_emitsOutputTail(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()
	logger := New(SourceFairway, Options{Store: store})

	const marker = "SYNC_TAIL_END"
	padding := strings.Repeat("p", DefaultOutputTailBytes*2)
	body := []byte(padding + marker)

	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		MarkResponseBody(r, body)
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sync")
	if err != nil {
		t.Fatal(err)
	}
	// Drain so the server handler goroutine — which emits the log line
	// after the handler returns — has time to complete before we read the
	// JSONL file. Without this, the test races the slog write.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	files, _ := filepath.Glob(filepath.Join(dir, SourceFairway, "*.jsonl"))
	if len(files) == 0 {
		t.Fatal("no log file written")
	}
	data, _ := os.ReadFile(files[0])
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tail, ok := rec["output_tail"].(string)
	if !ok {
		t.Fatalf("output_tail missing or wrong type: %v", rec["output_tail"])
	}
	if !strings.HasSuffix(tail, marker) {
		t.Fatalf("output_tail should end with marker; got tail ending %q",
			tail[max(0, len(tail)-len(marker)-10):])
	}
	if len(tail) > DefaultOutputTailBytes {
		t.Fatalf("output_tail = %d bytes, want ≤ %d", len(tail), DefaultOutputTailBytes)
	}
	if truncated, _ := rec["output_truncated"].(bool); !truncated {
		t.Errorf("output_truncated = %v, want true", rec["output_truncated"])
	}
}

// TestMiddleware_noMarkResponseBody_omitsOutputTail asserts that handlers
// that don't opt in do NOT get a spurious output_tail attribute — e.g.
// /_health, unmatched paths, http.forward proxies that prefer not to log
// the body. Preserves backward compat with existing log consumers.
func TestMiddleware_noMarkResponseBody_omitsOutputTail(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()
	logger := New(SourceFairway, Options{Store: store})

	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	files, _ := filepath.Glob(filepath.Join(dir, SourceFairway, "*.jsonl"))
	data, _ := os.ReadFile(files[0])
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := rec["output_tail"]; present {
		t.Errorf("output_tail should be omitted when handler didn't mark body; got %v", rec["output_tail"])
	}
}

func TestMiddlewareGeneratesTraceWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()
	logger := New(SourceFairway, Options{Store: store})

	var seen string
	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = trace.ID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/y")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen == "" {
		t.Fatal("expected generated trace id")
	}
	if got := resp.Header.Get(HeaderTraceID); got != seen {
		t.Fatalf("response trace mismatch: header=%q ctx=%q", got, seen)
	}
}
