package logs

import (
	"context"
	"encoding/json"
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
