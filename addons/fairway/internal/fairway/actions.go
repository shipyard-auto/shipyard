package fairway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"
)

// Result holds the outcome of executing a route action.
type Result struct {
	// HTTPStatus is the HTTP status code to return to the caller.
	HTTPStatus int

	// Body is the captured subprocess output (stdout+stderr combined) or the
	// proxied response body for http.forward.
	Body []byte

	// Header is only populated for http.forward — it contains the downstream
	// response headers.
	Header http.Header

	// Truncated is true when subprocess output exceeded MaxSubprocessOutput and
	// was capped.
	Truncated bool

	// ExitCode is the subprocess exit code. -1 when no subprocess was involved
	// (http.forward, pool timeout, context cancellation).
	ExitCode int

	// Duration is the wall-clock time from dispatch to completion.
	Duration time.Duration
}

// Executor dispatches route actions.
type Executor interface {
	Execute(ctx context.Context, route Route, req *http.Request) (Result, error)
}

// InFlightReporter exposes the number of currently running pooled subprocesses.
// It is optional and used by the socket status endpoint when available.
type InFlightReporter interface {
	InFlight() int
}

// SubprocessRunner creates an *exec.Cmd for the given command and arguments.
// It is injectable so tests can replace it with a fake.
type SubprocessRunner func(ctx context.Context, name string, args ...string) *exec.Cmd

// HTTPClient is an injectable http.Client interface used by http.forward actions.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// ExecutorConfig holds all tunable parameters for the executor.
type ExecutorConfig struct {
	// ShipyardBinary is the path to the shipyard binary. Defaults to "shipyard".
	ShipyardBinary string

	// MaxInFlight is the maximum number of concurrent subprocess executions.
	// Defaults to DefaultMaxInFlight.
	MaxInFlight int

	// QueueTimeout is how long a request waits for a pool slot before returning 503.
	// Defaults to DefaultQueueTimeout.
	QueueTimeout time.Duration

	// DefaultTimeout is the per-action timeout when route.Timeout is zero.
	// Defaults to DefaultActionTimeout.
	DefaultTimeout time.Duration

	// Run is the subprocess factory. Defaults to exec.CommandContext.
	Run SubprocessRunner

	// HTTP is the HTTP client for http.forward actions. Defaults to http.DefaultClient.
	HTTP HTTPClient

	// Now is the clock function. Defaults to time.Now.
	Now func() time.Time
}

type executor struct {
	cfg  ExecutorConfig
	pool chan struct{}
}

// NewExecutor creates an Executor from cfg, filling in any zero-value defaults.
func NewExecutor(cfg ExecutorConfig) *executor {
	if cfg.ShipyardBinary == "" {
		cfg.ShipyardBinary = "shipyard"
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = DefaultMaxInFlight
	}
	if cfg.QueueTimeout <= 0 {
		cfg.QueueTimeout = DefaultQueueTimeout
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = DefaultActionTimeout
	}
	if cfg.Run == nil {
		cfg.Run = exec.CommandContext
	}
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &executor{
		cfg:  cfg,
		pool: make(chan struct{}, cfg.MaxInFlight),
	}
}

// Execute dispatches the route action for the incoming request.
// http.forward routes bypass the subprocess pool.
func (e *executor) Execute(ctx context.Context, route Route, req *http.Request) (Result, error) {
	if route.Action.Type == ActionHTTPForward {
		return e.executeHTTPForward(ctx, route, req)
	}
	return e.executeSubprocess(ctx, route, req)
}

// InFlight returns the number of currently acquired subprocess slots.
func (e *executor) InFlight() int {
	return len(e.pool)
}

// executeSubprocess runs the shipyard CLI as a subprocess and returns the result.
func (e *executor) executeSubprocess(ctx context.Context, route Route, req *http.Request) (Result, error) {
	start := e.cfg.Now()

	// Acquire a worker pool slot. Return 503 if the queue is full for too long.
	select {
	case e.pool <- struct{}{}:
		// slot acquired — release it when we return
	case <-time.After(e.cfg.QueueTimeout):
		return Result{HTTPStatus: 503, ExitCode: -1, Duration: e.cfg.Now().Sub(start)}, nil
	case <-ctx.Done():
		return Result{HTTPStatus: 504, ExitCode: -1, Duration: e.cfg.Now().Sub(start)}, nil
	}
	defer func() { <-e.pool }()

	// Determine effective timeout for this action.
	timeout := e.cfg.DefaultTimeout
	if route.Timeout > 0 {
		timeout = route.Timeout
	}
	ctxCmd, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Read the request body once (limited), for actions that forward it.
	var bodyBytes []byte
	if req.Body != nil {
		lr := io.LimitReader(req.Body, MaxSubprocessOutput)
		bodyBytes, _ = io.ReadAll(lr)
	}

	args, stdin := buildArgs(route.Action, bodyBytes)

	cmd := e.cfg.Run(ctxCmd, e.cfg.ShipyardBinary, args...)

	lb := &limitedBuffer{limit: MaxSubprocessOutput}
	cmd.Stdout = lb
	cmd.Stderr = lb
	if stdin != nil {
		cmd.Stdin = stdin
	}

	runErr := cmd.Run()
	duration := e.cfg.Now().Sub(start)

	// Context deadline exceeded → 504.
	if ctxCmd.Err() == context.DeadlineExceeded {
		return Result{HTTPStatus: 504, Body: lb.Bytes(), Truncated: lb.Truncated, ExitCode: -1, Duration: duration}, nil
	}
	// External context cancelled → 504.
	if ctx.Err() != nil {
		return Result{HTTPStatus: 504, Body: lb.Bytes(), Truncated: lb.Truncated, ExitCode: -1, Duration: duration}, nil
	}

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return Result{
		HTTPStatus: mapExitCode(exitCode),
		Body:       lb.Bytes(),
		Truncated:  lb.Truncated,
		ExitCode:   exitCode,
		Duration:   duration,
	}, nil
}

// executeHTTPForward proxies the incoming request to the configured downstream URL.
// It does not use the subprocess worker pool.
func (e *executor) executeHTTPForward(ctx context.Context, route Route, req *http.Request) (Result, error) {
	start := e.cfg.Now()

	method := route.Action.Method
	if method == "" {
		method = req.Method
	}
	if method == "" {
		method = http.MethodPost
	}

	// Buffer the incoming body so the outgoing request has a concrete type
	// (*bytes.Reader) that net/http knows how to measure. Without this
	// net/http falls back to Transfer-Encoding: chunked, which some
	// downstreams (Python http.server, minimal HTTP/1.0 stacks) do not
	// accept — they read strictly by Content-Length and see an empty body.
	var bodyReader io.Reader
	if req.Body != nil {
		buf, err := io.ReadAll(io.LimitReader(req.Body, MaxSubprocessOutput+1))
		if err != nil {
			return Result{HTTPStatus: 400, ExitCode: -1, Duration: e.cfg.Now().Sub(start)}, nil
		}
		if int64(len(buf)) > MaxSubprocessOutput {
			return Result{HTTPStatus: 413, ExitCode: -1, Duration: e.cfg.Now().Sub(start)}, nil
		}
		bodyReader = bytes.NewReader(buf)
	}

	outReq, err := http.NewRequestWithContext(ctx, method, route.Action.URL, bodyReader)
	if err != nil {
		return Result{HTTPStatus: 502, ExitCode: -1, Duration: e.cfg.Now().Sub(start)}, nil
	}

	for k, v := range route.Action.Headers {
		outReq.Header.Set(k, v)
	}

	resp, err := e.cfg.HTTP.Do(outReq)
	if err != nil {
		return Result{HTTPStatus: 502, ExitCode: -1, Duration: e.cfg.Now().Sub(start)}, nil
	}
	defer resp.Body.Close()

	lb := &limitedBuffer{limit: MaxSubprocessOutput}
	_, _ = io.Copy(lb, resp.Body)

	return Result{
		HTTPStatus: resp.StatusCode,
		Body:       lb.Bytes(),
		Header:     resp.Header,
		Truncated:  lb.Truncated,
		ExitCode:   -1,
		Duration:   e.cfg.Now().Sub(start),
	}, nil
}

// mapExitCode converts a subprocess exit code to an HTTP status code.
//
//	0   → 200 OK
//	1   → 500 Internal Server Error
//	2   → 400 Bad Request
//	-1  → 504 Gateway Timeout (context or deadline)
//	other → 502 Bad Gateway
func mapExitCode(code int) int {
	switch code {
	case 0:
		return 200
	case 1:
		return 500
	case 2:
		return 400
	case -1:
		return 504
	default:
		return 502
	}
}

// buildArgs constructs the shipyard CLI arguments for the given action.
// For telegram.handle, the body is returned as an io.Reader to be set on cmd.Stdin.
func buildArgs(action Action, body []byte) (args []string, stdin io.Reader) {
	switch action.Type {
	case ActionCronRun:
		return []string{"cron", "run", action.Target}, nil
	case ActionCronEnable:
		return []string{"cron", "enable", action.Target}, nil
	case ActionCronDisable:
		return []string{"cron", "disable", action.Target}, nil
	case ActionServiceStart:
		return []string{"service", "start", action.Target}, nil
	case ActionServiceStop:
		return []string{"service", "stop", action.Target}, nil
	case ActionServiceRestart:
		return []string{"service", "restart", action.Target}, nil
	case ActionCrewRun:
		return []string{"crew", "run", action.Target}, nil
	case ActionMessageSend:
		return []string{"message", "send", fmt.Sprintf("--text=%s", string(body))}, nil
	case ActionTelegramHandle:
		return []string{"message", "telegram", "handle"}, bytes.NewReader(body)
	default:
		return []string{string(action.Type)}, nil
	}
}

// limitedBuffer is an io.Writer that caps the total bytes accepted.
// Writes beyond the limit are silently discarded and Truncated is set to true.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int64
	written   int64
	Truncated bool
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := lb.limit - lb.written
	if remaining <= 0 {
		lb.Truncated = true
		return n, nil
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
		lb.Truncated = true
	}
	actual, err := lb.buf.Write(p)
	lb.written += int64(actual)
	return n, err
}

// Bytes returns the captured (possibly truncated) output.
func (lb *limitedBuffer) Bytes() []byte {
	return lb.buf.Bytes()
}
