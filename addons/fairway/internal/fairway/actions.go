package fairway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Result represents the outcome of executing a Fairway action.
type Result struct {
	HTTPStatus int
	Body       []byte
	Header     http.Header
	Truncated  bool
	ExitCode   int
	Duration   time.Duration
}

// Executor runs route actions for authenticated requests.
type Executor interface {
	Execute(ctx context.Context, route Route, req *http.Request) (Result, error)
}

// SubprocessRunner constructs the command used to execute Shipyard CLI actions.
type SubprocessRunner func(ctx context.Context, name string, args ...string) *exec.Cmd

// HTTPClient performs outbound HTTP forwarding for http.forward actions.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// ExecutorConfig configures the action executor.
type ExecutorConfig struct {
	ShipyardBinary string
	MaxInFlight    int
	QueueTimeout   time.Duration
	DefaultTimeout time.Duration
	Run            SubprocessRunner
	HTTP           HTTPClient
	Now            func() time.Time
}

type executor struct {
	binary         string
	maxInFlight    int
	queueTimeout   time.Duration
	defaultTimeout time.Duration
	run            SubprocessRunner
	http           HTTPClient
	now            func() time.Time
	slots          chan struct{}
}

// NewExecutor builds the default Fairway action executor.
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
		cfg.HTTP = &http.Client{Timeout: cfg.DefaultTimeout}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	return &executor{
		binary:         cfg.ShipyardBinary,
		maxInFlight:    cfg.MaxInFlight,
		queueTimeout:   cfg.QueueTimeout,
		defaultTimeout: cfg.DefaultTimeout,
		run:            cfg.Run,
		http:           cfg.HTTP,
		now:            cfg.Now,
		slots:          make(chan struct{}, cfg.MaxInFlight),
	}
}

// Execute runs the action described by the route.
func (e *executor) Execute(ctx context.Context, route Route, req *http.Request) (Result, error) {
	if route.Action.Type == ActionHTTPForward {
		return e.executeHTTPForward(ctx, route, req)
	}
	return e.executeSubprocess(ctx, route, req)
}

func (e *executor) executeSubprocess(ctx context.Context, route Route, req *http.Request) (Result, error) {
	start := e.now()

	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return Result{HTTPStatus: http.StatusGatewayTimeout, ExitCode: -1, Duration: e.now().Sub(start)}, nil
	case <-time.After(e.queueTimeout):
		return Result{HTTPStatus: http.StatusServiceUnavailable, ExitCode: -1, Duration: e.now().Sub(start)}, nil
	}
	defer func() { <-e.slots }()

	body, bodyTruncated, err := readBounded(req.Body, MaxSubprocessOutput)
	if err != nil {
		return Result{}, err
	}

	timeout := route.Timeout
	if timeout <= 0 {
		timeout = e.defaultTimeout
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := buildActionArgs(route.Action, body)
	cmd := e.run(cmdCtx, e.binary, args...)
	if route.Action.Type == ActionTelegramHandle {
		cmd.Stdin = bytes.NewReader(body)
	}

	output := newBoundedBuffer(MaxSubprocessOutput)
	cmd.Stdout = output
	cmd.Stderr = output

	err = cmd.Run()
	duration := e.now().Sub(start)
	result := Result{
		Body:      output.Bytes(),
		Truncated: output.Truncated() || bodyTruncated,
		ExitCode:  -1,
		Duration:  duration,
	}

	if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
		result.HTTPStatus = http.StatusGatewayTimeout
		return result, nil
	}

	exitCode := extractExitCode(err, cmd)
	result.ExitCode = exitCode
	result.HTTPStatus = mapExitCode(exitCode)
	return result, nil
}

func (e *executor) executeHTTPForward(ctx context.Context, route Route, req *http.Request) (Result, error) {
	start := e.now()

	body, bodyTruncated, err := readBounded(req.Body, MaxSubprocessOutput)
	if err != nil {
		return Result{}, err
	}

	method := route.Action.Method
	if method == "" {
		method = http.MethodPost
	}

	outReq, err := http.NewRequestWithContext(ctx, method, route.Action.URL, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	for key, value := range route.Action.Headers {
		outReq.Header.Set(key, value)
	}

	resp, err := e.http.Do(outReq)
	if err != nil {
		return Result{
			HTTPStatus: http.StatusBadGateway,
			ExitCode:   -1,
			Duration:   e.now().Sub(start),
		}, nil
	}
	defer resp.Body.Close()

	respBody, respTruncated, err := readBounded(resp.Body, MaxSubprocessOutput)
	if err != nil {
		return Result{}, err
	}

	return Result{
		HTTPStatus: resp.StatusCode,
		Body:       respBody,
		Header:     resp.Header.Clone(),
		Truncated:  bodyTruncated || respTruncated,
		ExitCode:   -1,
		Duration:   e.now().Sub(start),
	}, nil
}

func buildActionArgs(action Action, body []byte) []string {
	switch action.Type {
	case ActionCronRun:
		return []string{"cron", "run", action.Target}
	case ActionCronEnable:
		return []string{"cron", "enable", action.Target}
	case ActionCronDisable:
		return []string{"cron", "disable", action.Target}
	case ActionServiceStart:
		return []string{"service", "start", action.Target}
	case ActionServiceStop:
		return []string{"service", "stop", action.Target}
	case ActionServiceRestart:
		return []string{"service", "restart", action.Target}
	case ActionMessageSend:
		return []string{"message", "send", string(body)}
	case ActionTelegramHandle:
		return []string{"message", "telegram", "handle"}
	default:
		return nil
	}
}

func mapExitCode(code int) int {
	switch code {
	case -1:
		return http.StatusGatewayTimeout
	case 0:
		return http.StatusOK
	case 1:
		return http.StatusInternalServerError
	case 2:
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}

func extractExitCode(runErr error, cmd *exec.Cmd) int {
	if runErr == nil {
		if cmd.ProcessState != nil {
			return cmd.ProcessState.ExitCode()
		}
		return 0
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func readBounded(r io.Reader, limit int) ([]byte, bool, error) {
	if r == nil {
		return nil, false, nil
	}

	data, err := io.ReadAll(io.LimitReader(r, int64(limit+1)))
	if err != nil {
		return nil, false, err
	}
	if len(data) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

type boundedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.buf.Len() >= b.limit {
		b.truncated = true
		return len(p), nil
	}

	remaining := b.limit - b.buf.Len()
	if len(p) > remaining {
		b.truncated = true
		_, _ = b.buf.Write(p[:remaining])
		return len(p), nil
	}

	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

func killProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGKILL)
	}
}
