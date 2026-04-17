package fairway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeHTTPClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (f fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return f.do(req)
}

func TestMapExitCode(t *testing.T) {
	tests := []struct {
		code int
		want int
	}{
		{-1, http.StatusGatewayTimeout},
		{0, http.StatusOK},
		{1, http.StatusInternalServerError},
		{2, http.StatusBadRequest},
		{3, http.StatusBadGateway},
	}
	for _, tt := range tests {
		if got := mapExitCode(tt.code); got != tt.want {
			t.Fatalf("mapExitCode(%d) = %d, want %d", tt.code, got, tt.want)
		}
	}
}

func TestBuildActionArgs(t *testing.T) {
	tests := []struct {
		name   string
		action Action
		body   []byte
		want   []string
	}{
		{name: "BuildArgs_cronRun_correctCLI", action: Action{Type: ActionCronRun, Target: "job-1"}, want: []string{"cron", "run", "job-1"}},
		{name: "BuildArgs_serviceRestart_correctCLI", action: Action{Type: ActionServiceRestart, Target: "svc-1"}, want: []string{"service", "restart", "svc-1"}},
		{name: "BuildArgs_messageSend_bodyBecomesText", action: Action{Type: ActionMessageSend}, body: []byte("hello"), want: []string{"message", "send", "hello"}},
		{name: "BuildArgs_telegramHandle_bodyViaStdin", action: Action{Type: ActionTelegramHandle}, want: []string{"message", "telegram", "handle"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildActionArgs(tt.action, tt.body)
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Fatalf("buildActionArgs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestExecutorSubprocess(t *testing.T) {
	t.Run("Exec_cronRun_happyPath", func(t *testing.T) {
		exec := newHelperExecutor(t, ExecutorConfig{})
		res, err := exec.Execute(context.Background(), testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if res.HTTPStatus != http.StatusOK || res.ExitCode != 0 || string(res.Body) != "ok\n" {
			t.Fatalf("result = %#v", res)
		}
	})

	t.Run("Exec_cronRun_exit1", func(t *testing.T) {
		exec := newHelperExecutor(t, ExecutorConfig{ShipyardBinary: "exit1"})
		res, _ := exec.Execute(context.Background(), testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		if res.HTTPStatus != http.StatusInternalServerError || res.ExitCode != 1 {
			t.Fatalf("result = %#v", res)
		}
	})

	t.Run("Exec_cronRun_exit2", func(t *testing.T) {
		exec := newHelperExecutor(t, ExecutorConfig{ShipyardBinary: "exit2"})
		res, _ := exec.Execute(context.Background(), testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		if res.HTTPStatus != http.StatusBadRequest || res.ExitCode != 2 {
			t.Fatalf("result = %#v", res)
		}
	})

	t.Run("Exec_cronRun_exitOther", func(t *testing.T) {
		exec := newHelperExecutor(t, ExecutorConfig{ShipyardBinary: "exit7"})
		res, _ := exec.Execute(context.Background(), testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		if res.HTTPStatus != http.StatusBadGateway || res.ExitCode != 7 {
			t.Fatalf("result = %#v", res)
		}
	})

	t.Run("Exec_timeout_returns504", func(t *testing.T) {
		exec := newHelperExecutor(t, ExecutorConfig{ShipyardBinary: "sleep", DefaultTimeout: 150 * time.Millisecond})
		route := testExecRoute(ActionCronRun)
		route.Timeout = 150 * time.Millisecond
		start := time.Now()
		res, _ := exec.Execute(context.Background(), route, httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		if res.HTTPStatus != http.StatusGatewayTimeout {
			t.Fatalf("result = %#v", res)
		}
		if time.Since(start) > 2500*time.Millisecond {
			t.Fatalf("timeout took too long: %s", time.Since(start))
		}
	})

	t.Run("Exec_stdoutTruncated", func(t *testing.T) {
		exec := newHelperExecutor(t, ExecutorConfig{ShipyardBinary: "large"})
		res, _ := exec.Execute(context.Background(), testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		if !res.Truncated || len(res.Body) != MaxSubprocessOutput {
			t.Fatalf("result = %#v", res)
		}
	})

	t.Run("Exec_respectRouteTimeoutOverride", func(t *testing.T) {
		exec := newHelperExecutor(t, ExecutorConfig{ShipyardBinary: "sleep", DefaultTimeout: time.Second})
		route := testExecRoute(ActionCronRun)
		route.Timeout = 150 * time.Millisecond
		start := time.Now()
		res, _ := exec.Execute(context.Background(), route, httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		if res.HTTPStatus != http.StatusGatewayTimeout {
			t.Fatalf("result = %#v", res)
		}
		if time.Since(start) > 2500*time.Millisecond {
			t.Fatalf("timeout override took too long: %s", time.Since(start))
		}
	})

	t.Run("Exec_contextCanceled_returns504", func(t *testing.T) {
		exec := newHelperExecutor(t, ExecutorConfig{ShipyardBinary: "sleep", DefaultTimeout: time.Second})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan Result, 1)
		go func() {
			res, _ := exec.Execute(ctx, testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
			done <- res
		}()
		time.Sleep(100 * time.Millisecond)
		cancel()
		res := <-done
		if res.HTTPStatus != http.StatusGatewayTimeout {
			t.Fatalf("result = %#v", res)
		}
	})
}

func TestExecutorPool(t *testing.T) {
	t.Run("Pool_limitsConcurrentExecutions", func(t *testing.T) {
		var current atomic.Int64
		var peak atomic.Int64
		execu := NewExecutor(ExecutorConfig{
			MaxInFlight: 4,
			Run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				cur := current.Add(1)
				for {
					old := peak.Load()
					if cur <= old || peak.CompareAndSwap(old, cur) {
						break
					}
				}
				cmd := helperCommand(t, "sleep")
				cmd.Env = append(cmd.Env, "FAIRWAY_HELPER_SLEEP_MS=200")
				return cmd
			},
		})

		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodPost, "http://example.com", nil)
				res, _ := execu.Execute(context.Background(), testExecRoute(ActionCronRun), req)
				if res.HTTPStatus != http.StatusServiceUnavailable {
					current.Add(-1)
				}
			}()
		}

		wg.Wait()
		if peak.Load() > 4 {
			t.Fatalf("peak concurrency = %d, want <= %d", peak.Load(), 4)
		}
	})

	t.Run("Pool_queueTimeout_returns503", func(t *testing.T) {
		execu := NewExecutor(ExecutorConfig{
			MaxInFlight:  1,
			QueueTimeout: 100 * time.Millisecond,
			Run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				cmd := helperCommand(t, "sleep")
				cmd.Env = append(cmd.Env, "FAIRWAY_HELPER_SLEEP_MS=400")
				return cmd
			},
		})

		firstDone := make(chan struct{})
		go func() {
			defer close(firstDone)
			_, _ = execu.Execute(context.Background(), testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		}()
		time.Sleep(100 * time.Millisecond)
		res, _ := execu.Execute(context.Background(), testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		if res.HTTPStatus != http.StatusServiceUnavailable {
			t.Fatalf("result = %#v", res)
		}
		<-firstDone
	})

	t.Run("Pool_releasesSlotOnError", func(t *testing.T) {
		exec := newHelperExecutor(t, ExecutorConfig{MaxInFlight: 1, ShipyardBinary: "exit1"})
		_, _ = exec.Execute(context.Background(), testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		res, _ := exec.Execute(context.Background(), testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		if res.ExitCode != 1 {
			t.Fatalf("result = %#v", res)
		}
	})
}

func TestExecutorHTTPForward(t *testing.T) {
	t.Run("HTTPForward_proxyRequest_passthroughStatus", func(t *testing.T) {
		exec := NewExecutor(ExecutorConfig{
			HTTP: fakeHTTPClient{do: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusAccepted,
					Header:     http.Header{"X-Test": []string{"1"}},
					Body:       io.NopCloser(strings.NewReader("forwarded")),
				}, nil
			}},
		})

		route := testForwardRoute()
		res, _ := exec.Execute(context.Background(), route, httptest.NewRequest(http.MethodPost, "http://example.com", strings.NewReader("body")))
		if res.HTTPStatus != http.StatusAccepted || string(res.Body) != "forwarded" {
			t.Fatalf("result = %#v", res)
		}
	})

	t.Run("HTTPForward_proxyRequest_copiesHeaders", func(t *testing.T) {
		exec := NewExecutor(ExecutorConfig{
			HTTP: fakeHTTPClient{do: func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("X-Route") != "1" {
					t.Fatalf("header X-Route = %q, want 1", req.Header.Get("X-Route"))
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
			}},
		})
		route := testForwardRoute()
		_, _ = exec.Execute(context.Background(), route, httptest.NewRequest(http.MethodPost, "http://example.com", strings.NewReader("body")))
	})

	t.Run("HTTPForward_proxyRequest_bodyBoundLimit", func(t *testing.T) {
		var captured []byte
		exec := NewExecutor(ExecutorConfig{
			HTTP: fakeHTTPClient{do: func(req *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(req.Body)
				captured = body
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("a", MaxSubprocessOutput+10)))}, nil
			}},
		})
		route := testForwardRoute()
		res, _ := exec.Execute(context.Background(), route, httptest.NewRequest(http.MethodPost, "http://example.com", bytes.NewReader(bytes.Repeat([]byte("b"), MaxSubprocessOutput+10))))
		if len(captured) != MaxSubprocessOutput || len(res.Body) != MaxSubprocessOutput || !res.Truncated {
			t.Fatalf("captured=%d body=%d truncated=%v", len(captured), len(res.Body), res.Truncated)
		}
	})

	t.Run("HTTPForward_targetUnreachable_returns502", func(t *testing.T) {
		exec := NewExecutor(ExecutorConfig{
			HTTP: fakeHTTPClient{do: func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("dial error")
			}},
		})
		res, _ := exec.Execute(context.Background(), testForwardRoute(), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		if res.HTTPStatus != http.StatusBadGateway {
			t.Fatalf("result = %#v", res)
		}
	})

	t.Run("HTTPForward_doesNotTouchSubprocessPool", func(t *testing.T) {
		block := make(chan struct{})
		exec := NewExecutor(ExecutorConfig{
			MaxInFlight:  1,
			QueueTimeout: time.Second,
			Run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				cmd := helperCommand(t, "sleep")
				cmd.Env = append(cmd.Env, "FAIRWAY_HELPER_SLEEP_MS=500")
				return cmd
			},
			HTTP: fakeHTTPClient{do: func(req *http.Request) (*http.Response, error) {
				close(block)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
			}},
		})

		go func() {
			_, _ = exec.Execute(context.Background(), testExecRoute(ActionCronRun), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		}()
		time.Sleep(100 * time.Millisecond)
		res, _ := exec.Execute(context.Background(), testForwardRoute(), httptest.NewRequest(http.MethodPost, "http://example.com", nil))
		<-block
		if res.HTTPStatus != http.StatusOK {
			t.Fatalf("result = %#v", res)
		}
	})
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	mode := os.Getenv("FAIRWAY_HELPER_MODE")
	switch mode {
	case "ok":
		_, _ = io.WriteString(os.Stdout, "ok\n")
		os.Exit(0)
	case "exit1":
		os.Exit(1)
	case "exit2":
		os.Exit(2)
	case "exit7":
		os.Exit(7)
	case "sleep":
		sleepMS, _ := strconv.Atoi(os.Getenv("FAIRWAY_HELPER_SLEEP_MS"))
		time.Sleep(time.Duration(sleepMS) * time.Millisecond)
		_, _ = io.WriteString(os.Stdout, "slept")
		os.Exit(0)
	case "large":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("a", MaxSubprocessOutput+1024))
		os.Exit(0)
	default:
		os.Exit(0)
	}
}

func newHelperExecutor(t *testing.T, cfg ExecutorConfig) *executor {
	t.Helper()
	mode := cfg.ShipyardBinary
	if mode == "" {
		mode = "ok"
	}
	cfg.Run = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := helperCommand(t, mode)
		if mode == "sleep" {
			cmd.Env = append(cmd.Env, "FAIRWAY_HELPER_SLEEP_MS=500")
		}
		return cmd
	}
	return NewExecutor(cfg)
}

func helperCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "FAIRWAY_HELPER_MODE="+mode)
	return cmd
}

func testExecRoute(actionType ActionType) Route {
	return Route{
		Path:   "/hooks/test",
		Auth:   Auth{Type: AuthBearer, Token: "secret"},
		Action: Action{Type: actionType, Target: "job-1"},
	}
}

func testForwardRoute() Route {
	return Route{
		Path: "/hooks/forward",
		Auth: Auth{Type: AuthBearer, Token: "secret"},
		Action: Action{
			Type:    ActionHTTPForward,
			URL:     "https://example.com/forward",
			Method:  http.MethodPost,
			Headers: map[string]string{"X-Route": "1"},
		},
	}
}
