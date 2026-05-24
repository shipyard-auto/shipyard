package fairway_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/logs/trace"
)

// recordedEntry pairs a slog.Record with the trace id derived from the ctx
// it was emitted under. Pulling trace from ctx mirrors the production
// Handler, which injects trace as an attr at write time.
type recordedEntry struct {
	record  slog.Record
	traceID string
}

// recordingHandler captures every slog.Record for assertion.
type recordingHandler struct {
	mu      sync.Mutex
	entries []recordedEntry
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, recordedEntry{record: r.Clone(), traceID: trace.ID(ctx)})
	return nil
}

func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *recordingHandler) snapshot() []recordedEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]recordedEntry, len(h.entries))
	copy(out, h.entries)
	return out
}

func newServerWithEventLogger(t *testing.T, exec fairway.Executor, routes ...fairway.Route) (*fairway.Server, *recordingHandler) {
	t.Helper()
	cfg := baseConfig()
	cfg.Routes = routes
	repo := &fakeRepo{cfg: cfg}
	router := fairway.NewRouterWithConfig(repo, cfg)

	rec := &recordingHandler{}
	logger := slog.New(rec)

	srv := fairway.NewServer(fairway.ServerConfig{
		Router:      router,
		Executor:    exec,
		EventLogger: logger,
	})
	return srv, rec
}

// TestEventLogger_syncEmitsHTTPRequest asserts the middleware emits exactly
// one structured "http_request" line for a sync route, with a trace id.
func TestEventLogger_syncEmitsHTTPRequest(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: []byte("ok")}}
	route := fairway.Route{
		Path:   "/sync",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	srv, rec := newServerWithEventLogger(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/sync", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	entries := rec.snapshot()
	if len(entries) != 1 {
		t.Fatalf("got %d records, want 1", len(entries))
	}
	if entries[0].record.Message != yardlogs.EventHTTPRequest {
		t.Fatalf("event = %q, want %q", entries[0].record.Message, yardlogs.EventHTTPRequest)
	}
	if traceID := w.Header().Get(yardlogs.HeaderTraceID); traceID == "" {
		t.Fatal("response missing X-Trace-Id header")
	}
}

// TestEventLogger_asyncCorrelatesByTraceID asserts the async path produces
// two records (the 202 from the middleware and async_dispatch_finished from
// the goroutine) with matching trace_id values.
func TestEventLogger_asyncCorrelatesByTraceID(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: []byte("ok"), ExitCode: 0}}
	route := fairway.Route{
		Path:    "/async",
		Async:   true,
		Auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
		Action:  fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
		Timeout: time.Second,
	}
	srv, rec := newServerWithEventLogger(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/async", strings.NewReader("{}"))
	r.Header.Set(yardlogs.HeaderTraceID, "deadbeefcafebabe")
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("async ack status = %d, want 202", w.Code)
	}

	// Wait for the async goroutine to complete and emit its record.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	entries := rec.snapshot()
	if len(entries) < 2 {
		t.Fatalf("got %d records, want at least 2", len(entries))
	}

	var http_, asyncFin *recordedEntry
	for i := range entries {
		switch entries[i].record.Message {
		case yardlogs.EventHTTPRequest:
			http_ = &entries[i]
		case yardlogs.EventAsyncDispatch:
			asyncFin = &entries[i]
		}
	}
	if http_ == nil {
		t.Fatal("missing http_request record")
	}
	if asyncFin == nil {
		t.Fatal("missing async_dispatch_finished record")
	}

	if http_.traceID == "" {
		t.Fatal("http_request record missing trace_id in ctx")
	}
	if http_.traceID != asyncFin.traceID {
		t.Fatalf("trace_id mismatch: http=%q async=%q", http_.traceID, asyncFin.traceID)
	}
	if http_.traceID != "deadbeefcafebabe" {
		t.Fatalf("inbound trace_id not propagated: got %q", http_.traceID)
	}
}

// TestEventLogger_sync_emitsOutputTail_onHTTPRequest asserts that on a
// synchronous route the http_request log line carries output_tail from the
// executor's Result.Body. Sync delivers the body to the caller, but the
// operator still wants a retrospective record in the JSONL.
func TestEventLogger_sync_emitsOutputTail_onHTTPRequest(t *testing.T) {
	t.Parallel()

	const marker = "SYNC_BODY_END"
	padding := strings.Repeat("s", yardlogs.DefaultOutputTailBytes*2)
	body := []byte(padding + marker)

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: body, ExitCode: 0}}
	route := fairway.Route{
		Path:   "/sync-tail",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
	srv, rec := newServerWithEventLogger(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/sync-tail", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("sync status = %d, want 200", w.Code)
	}

	var http_ *recordedEntry
	for _, e := range rec.snapshot() {
		if e.record.Message == yardlogs.EventHTTPRequest {
			ef := e
			http_ = &ef
			break
		}
	}
	if http_ == nil {
		t.Fatal("missing http_request record")
	}

	var tail string
	var sawTail bool
	http_.record.Attrs(func(a slog.Attr) bool {
		if a.Key == yardlogs.KeyOutputTail {
			tail = a.Value.String()
			sawTail = true
			return false
		}
		return true
	})
	if !sawTail {
		t.Fatal("output_tail missing on sync http_request")
	}
	if !strings.HasSuffix(tail, marker) {
		t.Fatalf("output_tail does not end with marker; tail tail = %q",
			tail[max(0, len(tail)-len(marker)-10):])
	}
	if len(tail) > yardlogs.DefaultOutputTailBytes {
		t.Fatalf("output_tail = %d bytes, want ≤ %d", len(tail), yardlogs.DefaultOutputTailBytes)
	}
}

// TestServeHTTP_asyncHTTPForward_realUpstream is the end-to-end integration
// test for F-03: a real production Executor (NewExecutor) is wired against
// a real httptest upstream, behind an async http.forward route. The client
// gets 202 immediately; the upstream is hit in the background; the log
// records the upstream status and body. Proves the whole pipeline
// (Validate accepts → server dispatches → executor forwards → log
// captures result) actually works together, not just in isolation.
func TestServeHTTP_asyncHTTPForward_realUpstream(t *testing.T) {
	t.Parallel()

	// Upstream server: records the inbound request body and answers 201.
	upstreamHits := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamHits <- body
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("upstream-ok"))
	}))
	defer upstream.Close()

	// Production executor, configured to call the upstream test server.
	realExec := fairway.NewExecutor(fairway.ExecutorConfig{
		HTTP: http.DefaultClient,
	})
	route := fairway.Route{
		Path:    "/notify",
		Async:   true,
		Auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
		Action:  fairway.Action{Type: fairway.ActionHTTPForward, URL: upstream.URL},
		Timeout: 5 * time.Second,
	}

	srv, rec := newServerWithEventLogger(t, realExec, route)
	handler := fairway.ServerHandlerForTest(srv)

	// Client request to fairway.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(`{"event":"deploy"}`))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	// 1. Client got 202 immediately (sync ack).
	if w.Code != http.StatusAccepted {
		t.Fatalf("client ack = %d, want 202", w.Code)
	}

	// 2. Upstream was eventually hit with the same body.
	select {
	case got := <-upstreamHits:
		if !strings.Contains(string(got), "deploy") {
			t.Fatalf("upstream body mismatch: got %q, want substring 'deploy'", string(got))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream was never called within 2s")
	}

	// 3. async_dispatch_finished log line records upstream status + body.
	deadline := time.Now().Add(2 * time.Second)
	var asyncFin *recordedEntry
	for time.Now().Before(deadline) {
		for _, e := range rec.snapshot() {
			if e.record.Message == yardlogs.EventAsyncDispatch {
				ef := e
				asyncFin = &ef
				break
			}
		}
		if asyncFin != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if asyncFin == nil {
		t.Fatal("async_dispatch_finished record never emitted")
	}

	var upstreamStatus int64
	var tail string
	var routeAction string
	asyncFin.record.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case yardlogs.KeyUpstreamHTTPStatus:
			upstreamStatus = a.Value.Int64()
		case yardlogs.KeyOutputTail:
			tail = a.Value.String()
		case yardlogs.KeyRouteAction:
			routeAction = a.Value.String()
		}
		return true
	})

	if routeAction != string(fairway.ActionHTTPForward) {
		t.Errorf("route_action = %q, want %q", routeAction, fairway.ActionHTTPForward)
	}
	if upstreamStatus != int64(http.StatusCreated) {
		t.Errorf("upstream_http_status = %d, want 201", upstreamStatus)
	}
	if !strings.Contains(tail, "upstream-ok") {
		t.Errorf("output_tail = %q, want substring 'upstream-ok'", tail)
	}
}

// TestEventLogger_asyncHTTPForward_emitsUpstreamStatus asserts that async
// http.forward (F-03) emits upstream_http_status carrying the real status
// the upstream answered with. Without this, the operator's only signal
// would be http_status=202 (the ack to the client) — they wouldn't know
// whether the fire-and-forget notification actually succeeded upstream.
func TestEventLogger_asyncHTTPForward_emitsUpstreamStatus(t *testing.T) {
	t.Parallel()

	// Fake executor returns the upstream's response — for http.forward,
	// Result.HTTPStatus is the upstream code, not the ack.
	exec := &fakeExecutor{result: fairway.Result{
		HTTPStatus: http.StatusCreated, // upstream answered 201
		Body:       []byte("created"),
		ExitCode:   -1, // http.forward has no subprocess
	}}
	route := fairway.Route{
		Path:    "/notify-async",
		Async:   true,
		Auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
		Action:  fairway.Action{Type: fairway.ActionHTTPForward, URL: "https://hooks.example.com"},
		Timeout: time.Second,
	}
	srv, rec := newServerWithEventLogger(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notify-async", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("client ack = %d, want 202", w.Code)
	}

	// Wait for async goroutine to write the dispatch record.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	var asyncFin *recordedEntry
	for _, e := range rec.snapshot() {
		if e.record.Message == yardlogs.EventAsyncDispatch {
			ef := e
			asyncFin = &ef
			break
		}
	}
	if asyncFin == nil {
		t.Fatal("missing async_dispatch_finished record")
	}

	var upstreamStatus int64
	var sawUpstream bool
	asyncFin.record.Attrs(func(a slog.Attr) bool {
		if a.Key == yardlogs.KeyUpstreamHTTPStatus {
			upstreamStatus = a.Value.Int64()
			sawUpstream = true
			return false
		}
		return true
	})
	if !sawUpstream {
		t.Fatal("upstream_http_status missing on async http.forward dispatch")
	}
	if upstreamStatus != int64(http.StatusCreated) {
		t.Fatalf("upstream_http_status = %d, want %d", upstreamStatus, http.StatusCreated)
	}

	// http_status stays at 202 — that's what the client received.
	var clientStatus int64
	asyncFin.record.Attrs(func(a slog.Attr) bool {
		if a.Key == yardlogs.KeyHTTPStatus {
			clientStatus = a.Value.Int64()
			return false
		}
		return true
	})
	if clientStatus != int64(http.StatusAccepted) {
		t.Errorf("http_status = %d, want 202 (the client ack, not upstream)", clientStatus)
	}
}

// TestEventLogger_async_cronRun_omitsUpstreamStatus is the regression guard
// for non-forward async actions: upstream_http_status is meaningful only
// for http.forward, so cron/crew async dispatches must NOT carry it.
func TestEventLogger_async_cronRun_omitsUpstreamStatus(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: []byte("ok"), ExitCode: 0}}
	route := fairway.Route{
		Path:    "/cron-async",
		Async:   true,
		Auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
		Action:  fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
		Timeout: time.Second,
	}
	srv, rec := newServerWithEventLogger(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/cron-async", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, e := range rec.snapshot() {
		if e.record.Message != yardlogs.EventAsyncDispatch {
			continue
		}
		var sawUpstream bool
		e.record.Attrs(func(a slog.Attr) bool {
			if a.Key == yardlogs.KeyUpstreamHTTPStatus {
				sawUpstream = true
				return false
			}
			return true
		})
		if sawUpstream {
			t.Fatal("upstream_http_status should NOT appear on cron.run async dispatch")
		}
		return
	}
	t.Fatal("missing async_dispatch_finished record")
}

// TestEventLogger_async_emptyBody_omitsOutputTail asserts the symmetrical
// case: when the executor returned no bytes (e.g. timeout, exec error
// before any output), async_dispatch_finished does NOT carry a spurious
// empty output_tail. Keeps the JSONL clean.
func TestEventLogger_async_emptyBody_omitsOutputTail(t *testing.T) {
	t.Parallel()

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: nil, ExitCode: 0}}
	route := fairway.Route{
		Path:    "/async-empty",
		Async:   true,
		Auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
		Action:  fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
		Timeout: time.Second,
	}
	srv, rec := newServerWithEventLogger(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/async-empty", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, e := range rec.snapshot() {
		if e.record.Message != yardlogs.EventAsyncDispatch {
			continue
		}
		var sawTail bool
		e.record.Attrs(func(a slog.Attr) bool {
			if a.Key == yardlogs.KeyOutputTail {
				sawTail = true
				return false
			}
			return true
		})
		if sawTail {
			t.Fatal("output_tail should be omitted when body is empty")
		}
		return
	}
	t.Fatal("missing async_dispatch_finished record")
}

// TestEventLogger_async_emitsOutputTail asserts the async_dispatch_finished
// record carries the tail of the executor body. On async, the 202 was sent
// before the body existed, so without output_tail the operator has no
// retrospective view of what cron.run / crew.run / http.forward produced.
func TestEventLogger_async_emitsOutputTail(t *testing.T) {
	t.Parallel()

	const marker = "ASYNC_END"
	padding := strings.Repeat("z", yardlogs.DefaultOutputTailBytes*2)
	body := []byte(padding + marker)

	exec := &fakeExecutor{result: fairway.Result{HTTPStatus: 200, Body: body, ExitCode: 0, Truncated: true}}
	route := fairway.Route{
		Path:    "/async-tail",
		Async:   true,
		Auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
		Action:  fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
		Timeout: time.Second,
	}
	srv, rec := newServerWithEventLogger(t, exec, route)
	handler := fairway.ServerHandlerForTest(srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/async-tail", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1"
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("async ack status = %d, want 202", w.Code)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	var asyncFin *recordedEntry
	for _, e := range rec.snapshot() {
		if e.record.Message == yardlogs.EventAsyncDispatch {
			ef := e
			asyncFin = &ef
			break
		}
	}
	if asyncFin == nil {
		t.Fatal("missing async_dispatch_finished record")
	}

	var tail string
	var sawTail bool
	asyncFin.record.Attrs(func(a slog.Attr) bool {
		if a.Key == yardlogs.KeyOutputTail {
			tail = a.Value.String()
			sawTail = true
			return false
		}
		return true
	})
	if !sawTail {
		t.Fatal("output_tail missing on async_dispatch_finished")
	}
	if !strings.HasSuffix(tail, marker) {
		t.Fatalf("output_tail does not end with marker; tail tail = %q", tail[max(0, len(tail)-len(marker)-10):])
	}
	if len(tail) > yardlogs.DefaultOutputTailBytes {
		t.Fatalf("output_tail = %d bytes, want ≤ %d", len(tail), yardlogs.DefaultOutputTailBytes)
	}
}
