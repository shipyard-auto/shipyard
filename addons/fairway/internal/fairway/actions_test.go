package fairway_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// ── TestMain: helper process pattern ─────────────────────────────────────────
//
// When GO_FAIRWAY_HELPER=1, this binary runs as a fake "shipyard" subprocess
// controlled by environment variables. This avoids any real system calls in
// executor tests.

func TestMain(m *testing.M) {
	if os.Getenv("GO_FAIRWAY_HELPER") == "1" {
		runHelperProcess()
		// runHelperProcess always calls os.Exit — this is unreachable.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runHelperProcess() {
	outputSize, _ := strconv.Atoi(os.Getenv("GO_HELPER_OUTPUT_SIZE"))
	sleepMs, _ := strconv.Atoi(os.Getenv("GO_HELPER_SLEEP_MS"))
	exitCode, _ := strconv.Atoi(os.Getenv("GO_HELPER_EXIT"))
	echoStdin := os.Getenv("GO_HELPER_ECHO_STDIN") == "1"

	if outputSize > 0 {
		chunk := bytes.Repeat([]byte("X"), 4096)
		written := 0
		for written < outputSize {
			toWrite := outputSize - written
			if toWrite > 4096 {
				toWrite = 4096
			}
			os.Stdout.Write(chunk[:toWrite]) //nolint:errcheck
			written += toWrite
		}
	}

	if echoStdin {
		io.Copy(os.Stdout, os.Stdin) //nolint:errcheck
	}

	if sleepMs > 0 {
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	}

	os.Exit(exitCode)
}

// helperRunner returns a SubprocessRunner that invokes this test binary as a
// fake shipyard process controlled by env vars.
func helperRunner(env ...string) fairway.SubprocessRunner {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// -test.run=^$ ensures no real tests run in the helper process;
		// TestMain still fires and sees GO_FAIRWAY_HELPER=1.
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), append([]string{"GO_FAIRWAY_HELPER=1"}, env...)...)
		return cmd
	}
}

// capturingRunner records the name and args passed to each invocation, then
// runs as a helper subprocess that exits 0 immediately.
type capturingRunner struct {
	mu     sync.Mutex
	calls  []capturedCall
	runner fairway.SubprocessRunner
}

type capturedCall struct {
	Name string
	Args []string
}

func newCapturingRunner(extraEnv ...string) *capturingRunner {
	cr := &capturingRunner{}
	base := helperRunner(extraEnv...)
	cr.runner = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cr.mu.Lock()
		cr.calls = append(cr.calls, capturedCall{Name: name, Args: args})
		cr.mu.Unlock()
		return base(ctx, name, args...)
	}
	return cr
}

func (cr *capturingRunner) lastCall() capturedCall {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if len(cr.calls) == 0 {
		return capturedCall{}
	}
	return cr.calls[len(cr.calls)-1]
}

// ── fakeHTTPClient ────────────────────────────────────────────────────────────

type fakeHTTPClient struct {
	resp *http.Response
	err  error
}

func (f *fakeHTTPClient) Do(_ *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func staticHTTPResponse(status int, body string, headers http.Header) *http.Response {
	rec := httptest.NewRecorder()
	rec.WriteHeader(status)
	rec.WriteString(body)
	resp := rec.Result()
	for k, vs := range headers {
		for _, v := range vs {
			resp.Header.Set(k, v)
		}
	}
	return resp
}

// ── route helpers ─────────────────────────────────────────────────────────────

func subprocessRoute(actionType fairway.ActionType, target string) fairway.Route {
	return fairway.Route{
		Path: "/test",
		Auth: fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{
			Type:   actionType,
			Target: target,
		},
	}
}

func forwardRoute(url, method string, headers map[string]string) fairway.Route {
	return fairway.Route{
		Path: "/fwd",
		Auth: fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{
			Type:    fairway.ActionHTTPForward,
			URL:     url,
			Method:  method,
			Headers: headers,
		},
	}
}

func defaultExec(run fairway.SubprocessRunner) *fairway.ExecutorConfig {
	cfg := &fairway.ExecutorConfig{
		MaxInFlight:    4,
		QueueTimeout:   200 * time.Millisecond,
		DefaultTimeout: 5 * time.Second,
		Run:            run,
		HTTP:           &fakeHTTPClient{resp: staticHTTPResponse(200, "ok", nil)},
	}
	return cfg
}

// ── Subprocess: exit code mapping ─────────────────────────────────────────────

func TestMapExitCode_tableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		env        string
		wantStatus int
	}{
		{"Exec_cronRun_happyPath", "GO_HELPER_EXIT=0", 200},
		{"Exec_cronRun_exit1", "GO_HELPER_EXIT=1", 500},
		{"Exec_cronRun_exit2", "GO_HELPER_EXIT=2", 400},
		{"Exec_cronRun_exitOther", "GO_HELPER_EXIT=42", 502},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := defaultExec(helperRunner(tc.env))
			e := fairway.NewExecutor(*cfg)

			route := subprocessRoute(fairway.ActionCronRun, "job-1")
			req := httptest.NewRequest(http.MethodPost, "/test", nil)

			result, err := e.Execute(context.Background(), route, req)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result.HTTPStatus != tc.wantStatus {
				t.Errorf("HTTPStatus = %d; want %d", result.HTTPStatus, tc.wantStatus)
			}
		})
	}
}

// ── Subprocess: timeout ───────────────────────────────────────────────────────

func TestExec_timeout_returns504(t *testing.T) {
	t.Parallel()

	cfg := defaultExec(helperRunner("GO_HELPER_SLEEP_MS=5000", "GO_HELPER_EXIT=0"))
	cfg.DefaultTimeout = 100 * time.Millisecond
	e := fairway.NewExecutor(*cfg)

	route := subprocessRoute(fairway.ActionCronRun, "job-1")
	req := httptest.NewRequest(http.MethodPost, "/test", nil)

	start := time.Now()
	result, err := e.Execute(context.Background(), route, req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.HTTPStatus != 504 {
		t.Errorf("HTTPStatus = %d; want 504", result.HTTPStatus)
	}
	if elapsed > 1*time.Second {
		t.Errorf("took too long: %s; timeout should have fired at ~100ms", elapsed)
	}
}

func TestExec_respectRouteTimeoutOverride(t *testing.T) {
	t.Parallel()

	cfg := defaultExec(helperRunner("GO_HELPER_SLEEP_MS=5000", "GO_HELPER_EXIT=0"))
	cfg.DefaultTimeout = 30 * time.Second // long default
	e := fairway.NewExecutor(*cfg)

	route := subprocessRoute(fairway.ActionCronRun, "job-1")
	route.Timeout = 100 * time.Millisecond // short route-level override

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	start := time.Now()
	result, _ := e.Execute(context.Background(), route, req)
	elapsed := time.Since(start)

	if result.HTTPStatus != 504 {
		t.Errorf("HTTPStatus = %d; want 504", result.HTTPStatus)
	}
	if elapsed > 1*time.Second {
		t.Errorf("route timeout not respected: elapsed %s", elapsed)
	}
}

func TestExec_contextCanceled_returns504(t *testing.T) {
	t.Parallel()

	cfg := defaultExec(helperRunner("GO_HELPER_SLEEP_MS=5000", "GO_HELPER_EXIT=0"))
	e := fairway.NewExecutor(*cfg)

	route := subprocessRoute(fairway.ActionCronRun, "job-1")
	req := httptest.NewRequest(http.MethodPost, "/test", nil)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan fairway.Result, 1)
	go func() {
		r, _ := e.Execute(ctx, route, req)
		done <- r
	}()

	// Cancel before the subprocess finishes.
	time.Sleep(30 * time.Millisecond)
	cancel()

	result := <-done
	if result.HTTPStatus != 504 {
		t.Errorf("HTTPStatus = %d; want 504 on context cancel", result.HTTPStatus)
	}
}

// ── Subprocess: output truncation ─────────────────────────────────────────────

func TestExec_stdoutTruncated(t *testing.T) {
	t.Parallel()

	// Generate 5MB > MaxSubprocessOutput (4MB).
	const outputSize = 5 * 1024 * 1024
	cfg := defaultExec(helperRunner(
		"GO_HELPER_OUTPUT_SIZE="+strconv.Itoa(outputSize),
		"GO_HELPER_EXIT=0",
	))
	cfg.DefaultTimeout = 10 * time.Second
	e := fairway.NewExecutor(*cfg)

	route := subprocessRoute(fairway.ActionCronRun, "job-1")
	req := httptest.NewRequest(http.MethodPost, "/test", nil)

	result, err := e.Execute(context.Background(), route, req)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Truncated {
		t.Error("expected Truncated=true for output > 4MB")
	}
	if int64(len(result.Body)) > fairway.MaxSubprocessOutput {
		t.Errorf("body length %d exceeds MaxSubprocessOutput %d", len(result.Body), fairway.MaxSubprocessOutput)
	}
	if int64(len(result.Body)) != fairway.MaxSubprocessOutput {
		t.Errorf("body length = %d; want exactly %d", len(result.Body), fairway.MaxSubprocessOutput)
	}
}

// ── Pool ──────────────────────────────────────────────────────────────────────

func TestPool_limitsConcurrentExecutions(t *testing.T) {
	t.Parallel()

	// Strategy: launch (maxInFlight + extra) goroutines simultaneously.
	// Subprocesses block for 5s; QueueTimeout is short.
	// Expected: exactly `extra` goroutines get 503 (queue exhausted).
	// This verifies the pool bound without touching cmd internals (avoiding races).
	const maxInFlight = 3
	const extra = 3
	const total = maxInFlight + extra

	cfg := fairway.ExecutorConfig{
		MaxInFlight:    maxInFlight,
		QueueTimeout:   80 * time.Millisecond,
		DefaultTimeout: 5 * time.Second,
		Run:            helperRunner("GO_HELPER_SLEEP_MS=5000"),
		HTTP:           &fakeHTTPClient{},
	}
	e := fairway.NewExecutor(cfg)

	route := subprocessRoute(fairway.ActionCronRun, "job-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	statuses := make(chan int, total)
	for i := 0; i < total; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/test", nil)
			r, _ := e.Execute(ctx, route, req)
			statuses <- r.HTTPStatus
		}()
	}

	// Collect results. 503s arrive after QueueTimeout; 504s after cancel.
	var count503 int
	collected := 0
	deadline := time.After(5 * time.Second)
	for collected < total {
		select {
		case s := <-statuses:
			if s == 503 {
				count503++
			}
			collected++
			// Once we've seen all the 503s, cancel the blocked goroutines.
			if count503 >= extra {
				cancel()
			}
		case <-deadline:
			t.Fatalf("timed out collecting results (got %d/%d, count503=%d)", collected, total, count503)
		}
	}

	if count503 != extra {
		t.Errorf("got %d 503 responses; want %d (pool size=%d, total goroutines=%d)",
			count503, extra, maxInFlight, total)
	}
}

func TestPool_queueTimeout_returns503(t *testing.T) {
	t.Parallel()

	const poolSize = 1

	// Signal fired when the pool slot is acquired by the first goroutine.
	slotAcquired := make(chan struct{}, 1)

	blockingRunner := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		slotAcquired <- struct{}{} // pool slot acquired before runner is called
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "GO_FAIRWAY_HELPER=1", "GO_HELPER_SLEEP_MS=5000")
		return cmd
	}

	cfg := fairway.ExecutorConfig{
		MaxInFlight:    poolSize,
		QueueTimeout:   80 * time.Millisecond,
		DefaultTimeout: 3 * time.Second,
		Run:            blockingRunner,
		HTTP:           &fakeHTTPClient{},
	}
	e := fairway.NewExecutor(cfg)

	route := subprocessRoute(fairway.ActionCronRun, "job-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First goroutine fills the pool.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodPost, "/test", nil)
		e.Execute(ctx, route, req) //nolint:errcheck
	}()

	// Wait until the slot is acquired.
	select {
	case <-slotAcquired:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for pool slot")
	}

	// Second request should time out waiting for the pool → 503.
	req2 := httptest.NewRequest(http.MethodPost, "/test", nil)
	result, err := e.Execute(context.Background(), route, req2)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d; want 503", result.HTTPStatus)
	}

	cancel()
	wg.Wait()
}

func TestPool_releasesSlotOnError(t *testing.T) {
	t.Parallel()

	cfg := fairway.ExecutorConfig{
		MaxInFlight:    1,
		QueueTimeout:   200 * time.Millisecond,
		DefaultTimeout: 500 * time.Millisecond,
		Run:            helperRunner("GO_HELPER_EXIT=1"),
		HTTP:           &fakeHTTPClient{},
	}
	e := fairway.NewExecutor(cfg)

	route := subprocessRoute(fairway.ActionCronRun, "job-1")
	req := httptest.NewRequest(http.MethodPost, "/test", nil)

	// First execution fails with exit 1.
	r1, _ := e.Execute(context.Background(), route, req)
	if r1.HTTPStatus != 500 {
		t.Fatalf("first Execute() = %d; want 500", r1.HTTPStatus)
	}

	// Slot must be released; second execution should succeed immediately.
	req2 := httptest.NewRequest(http.MethodPost, "/test", nil)
	r2, _ := e.Execute(context.Background(), route, req2)
	if r2.HTTPStatus != 500 {
		t.Errorf("second Execute() = %d; want 500 (slot should have been released)", r2.HTTPStatus)
	}
}

// ── HTTP Forward ──────────────────────────────────────────────────────────────

func TestHTTPForward_proxyRequest_passthroughStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []int{200, 201, 400, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			cfg := defaultExec(helperRunner())
			cfg.HTTP = &fakeHTTPClient{resp: staticHTTPResponse(status, "body", nil)}
			e := fairway.NewExecutor(*cfg)

			route := forwardRoute("http://example.com/hook", http.MethodPost, nil)
			req := httptest.NewRequest(http.MethodPost, "/fwd", strings.NewReader("payload"))

			result, _ := e.Execute(context.Background(), route, req)
			if result.HTTPStatus != status {
				t.Errorf("HTTPStatus = %d; want %d", result.HTTPStatus, status)
			}
		})
	}
}

func TestHTTPForward_proxyRequest_copiesHeaders(t *testing.T) {
	t.Parallel()

	var capturedReq *http.Request
	client := &captureHTTPClient{fn: func(r *http.Request) (*http.Response, error) {
		capturedReq = r
		return staticHTTPResponse(200, "", nil), nil
	}}

	cfg := defaultExec(helperRunner())
	cfg.HTTP = client
	e := fairway.NewExecutor(*cfg)

	route := forwardRoute("http://example.com/hook", http.MethodPost, map[string]string{
		"X-Custom-Header": "custom-value",
	})
	req := httptest.NewRequest(http.MethodPost, "/fwd", nil)
	e.Execute(context.Background(), route, req) //nolint:errcheck

	if capturedReq == nil {
		t.Fatal("HTTP client was never called")
	}
	if capturedReq.Header.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("X-Custom-Header = %q; want %q", capturedReq.Header.Get("X-Custom-Header"), "custom-value")
	}
}

func TestHTTPForward_proxyRequest_bodyBoundLimit(t *testing.T) {
	t.Parallel()

	// Response body > MaxSubprocessOutput should be truncated.
	bigBody := strings.Repeat("x", int(fairway.MaxSubprocessOutput)+1024)
	cfg := defaultExec(helperRunner())
	cfg.HTTP = &fakeHTTPClient{resp: staticHTTPResponse(200, bigBody, nil)}
	e := fairway.NewExecutor(*cfg)

	route := forwardRoute("http://example.com/hook", http.MethodPost, nil)
	req := httptest.NewRequest(http.MethodPost, "/fwd", nil)

	result, _ := e.Execute(context.Background(), route, req)
	if !result.Truncated {
		t.Error("expected Truncated=true for large downstream response")
	}
	if int64(len(result.Body)) > fairway.MaxSubprocessOutput {
		t.Errorf("body %d > MaxSubprocessOutput %d", len(result.Body), fairway.MaxSubprocessOutput)
	}
}

func TestHTTPForward_targetUnreachable_returns502(t *testing.T) {
	t.Parallel()

	cfg := defaultExec(helperRunner())
	cfg.HTTP = &fakeHTTPClient{err: fmt.Errorf("connection refused")}
	e := fairway.NewExecutor(*cfg)

	route := forwardRoute("http://127.0.0.1:1/hook", http.MethodPost, nil)
	req := httptest.NewRequest(http.MethodPost, "/fwd", nil)

	result, _ := e.Execute(context.Background(), route, req)
	if result.HTTPStatus != 502 {
		t.Errorf("HTTPStatus = %d; want 502", result.HTTPStatus)
	}
}

func TestHTTPForward_doesNotTouchSubprocessPool(t *testing.T) {
	t.Parallel()

	// Fill the pool completely with blocking subprocesses.
	slotsFilled := make(chan struct{}, 4)
	blockingRun := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		slotsFilled <- struct{}{}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "GO_FAIRWAY_HELPER=1", "GO_HELPER_SLEEP_MS=5000")
		return cmd
	}

	cfg := fairway.ExecutorConfig{
		MaxInFlight:    2,
		QueueTimeout:   50 * time.Millisecond,
		DefaultTimeout: 3 * time.Second,
		Run:            blockingRun,
		HTTP:           &fakeHTTPClient{resp: staticHTTPResponse(200, "ok", nil)},
	}
	e := fairway.NewExecutor(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fill the pool.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/test", nil)
			e.Execute(ctx, subprocessRoute(fairway.ActionCronRun, "j"), req) //nolint:errcheck
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-slotsFilled:
		case <-time.After(3 * time.Second):
			t.Fatal("pool did not fill in time")
		}
	}

	// http.forward must succeed even with pool full.
	route := forwardRoute("http://example.com/hook", http.MethodPost, nil)
	req := httptest.NewRequest(http.MethodPost, "/fwd", nil)
	result, _ := e.Execute(context.Background(), route, req)
	if result.HTTPStatus != 200 {
		t.Errorf("http.forward HTTPStatus = %d; want 200 (should bypass pool)", result.HTTPStatus)
	}

	cancel()
	wg.Wait()
}

// ── BuildArgs ─────────────────────────────────────────────────────────────────

func TestBuildArgs_cronRun_correctCLI(t *testing.T) {
	t.Parallel()

	cr := newCapturingRunner()
	cfg := defaultExec(cr.runner)
	e := fairway.NewExecutor(*cfg)

	route := subprocessRoute(fairway.ActionCronRun, "nightly-backup")
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	e.Execute(context.Background(), route, req) //nolint:errcheck

	call := cr.lastCall()
	if len(call.Args) < 3 || call.Args[0] != "cron" || call.Args[1] != "run" || call.Args[2] != "nightly-backup" {
		t.Errorf("args = %v; want [cron run nightly-backup ...]", call.Args)
	}
}

func TestBuildArgs_serviceRestart_correctCLI(t *testing.T) {
	t.Parallel()

	cr := newCapturingRunner()
	cfg := defaultExec(cr.runner)
	e := fairway.NewExecutor(*cfg)

	route := subprocessRoute(fairway.ActionServiceRestart, "my-svc")
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	e.Execute(context.Background(), route, req) //nolint:errcheck

	call := cr.lastCall()
	if len(call.Args) < 3 || call.Args[0] != "service" || call.Args[1] != "restart" || call.Args[2] != "my-svc" {
		t.Errorf("args = %v; want [service restart my-svc ...]", call.Args)
	}
}

func TestBuildArgs_messageSend_bodyBecomesText(t *testing.T) {
	t.Parallel()

	cr := newCapturingRunner()
	cfg := defaultExec(cr.runner)
	e := fairway.NewExecutor(*cfg)

	route := fairway.Route{
		Path:   "/msg",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionMessageSend},
	}
	req := httptest.NewRequest(http.MethodPost, "/msg", strings.NewReader("hello world"))
	e.Execute(context.Background(), route, req) //nolint:errcheck

	call := cr.lastCall()
	found := false
	for _, arg := range call.Args {
		if strings.HasPrefix(arg, "--text=") {
			found = true
			if !strings.Contains(arg, "hello world") {
				t.Errorf("--text arg = %q; want to contain 'hello world'", arg)
			}
		}
	}
	if !found {
		t.Errorf("args = %v; expected --text= argument", call.Args)
	}
}

func TestBuildArgs_telegramHandle_bodyViaStdin(t *testing.T) {
	t.Parallel()

	// telegram.handle should pass body via stdin and echo it to stdout.
	echoRunner := helperRunner("GO_HELPER_ECHO_STDIN=1", "GO_HELPER_EXIT=0")
	cfg := fairway.ExecutorConfig{
		MaxInFlight:    4,
		QueueTimeout:   200 * time.Millisecond,
		DefaultTimeout: 5 * time.Second,
		Run:            echoRunner,
		HTTP:           &fakeHTTPClient{},
	}
	e := fairway.NewExecutor(cfg)

	route := fairway.Route{
		Path:   "/tg",
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionTelegramHandle},
	}
	req := httptest.NewRequest(http.MethodPost, "/tg", strings.NewReader("telegram-payload"))
	result, err := e.Execute(context.Background(), route, req)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(string(result.Body), "telegram-payload") {
		t.Errorf("body = %q; expected stdin ('telegram-payload') echoed to stdout", result.Body)
	}
}

// ── BuildArgs: remaining action types ────────────────────────────────────────

func TestBuildArgs_allActionTypes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      fairway.ActionType
		target    string
		wantArgs0 string
		wantArgs1 string
	}{
		{fairway.ActionCronEnable, "j", "cron", "enable"},
		{fairway.ActionCronDisable, "j", "cron", "disable"},
		{fairway.ActionServiceStart, "s", "service", "start"},
		{fairway.ActionServiceStop, "s", "service", "stop"},
	}

	for _, tc := range cases {
		t.Run(string(tc.name), func(t *testing.T) {
			t.Parallel()
			cr := newCapturingRunner()
			cfg := fairway.ExecutorConfig{
				MaxInFlight:    4,
				QueueTimeout:   200 * time.Millisecond,
				DefaultTimeout: 5 * time.Second,
				Run:            cr.runner,
				HTTP:           &fakeHTTPClient{},
			}
			e := fairway.NewExecutor(cfg)

			route := subprocessRoute(tc.name, tc.target)
			req := httptest.NewRequest(http.MethodPost, "/test", nil)
			e.Execute(context.Background(), route, req) //nolint:errcheck

			call := cr.lastCall()
			if len(call.Args) < 2 || call.Args[0] != tc.wantArgs0 || call.Args[1] != tc.wantArgs1 {
				t.Errorf("args = %v; want [%s %s ...]", call.Args, tc.wantArgs0, tc.wantArgs1)
			}
		})
	}
}

// ── mapExitCode: -1 case ──────────────────────────────────────────────────────

func TestMapExitCode_minusOne_returns504(t *testing.T) {
	t.Parallel()

	// Force a context cancellation so the executor uses the -1 path.
	cfg := fairway.ExecutorConfig{
		MaxInFlight:    4,
		QueueTimeout:   200 * time.Millisecond,
		DefaultTimeout: 5 * time.Second,
		Run:            helperRunner("GO_HELPER_SLEEP_MS=5000"),
		HTTP:           &fakeHTTPClient{},
	}
	e := fairway.NewExecutor(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	route := subprocessRoute(fairway.ActionCronRun, "job")
	req := httptest.NewRequest(http.MethodPost, "/test", nil)

	done := make(chan fairway.Result, 1)
	go func() {
		r, _ := e.Execute(ctx, route, req)
		done <- r
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	r := <-done
	if r.HTTPStatus != 504 {
		t.Errorf("HTTPStatus = %d; want 504 for context cancel (-1 exit)", r.HTTPStatus)
	}
}

// ── NewExecutor: default values ───────────────────────────────────────────────

func TestNewExecutor_defaults(t *testing.T) {
	t.Parallel()

	// Passing zero-value config should not panic and should use defaults.
	e := fairway.NewExecutor(fairway.ExecutorConfig{})
	if e == nil {
		t.Fatal("NewExecutor() returned nil")
	}

	// A basic execution with default runner would call real "shipyard" which
	// likely doesn't exist in CI, so we just verify construction succeeds.
}

// ── captureHTTPClient ─────────────────────────────────────────────────────────

type captureHTTPClient struct {
	fn func(*http.Request) (*http.Response, error)
}

func (c *captureHTTPClient) Do(r *http.Request) (*http.Response, error) {
	return c.fn(r)
}
