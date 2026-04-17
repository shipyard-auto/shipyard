package fairway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

type fakeExecutor struct {
	execute func(context.Context, Route, *http.Request) (Result, error)
}

func (f fakeExecutor) Execute(ctx context.Context, route Route, req *http.Request) (Result, error) {
	return f.execute(ctx, route, req)
}

func TestNewServer(t *testing.T) {
	t.Run("rejectsNilRouter", func(t *testing.T) {
		_, err := NewServer(ServerConfig{Executor: fakeExecutor{execute: func(context.Context, Route, *http.Request) (Result, error) {
			return Result{}, nil
		}}})
		if err == nil {
			t.Fatal("NewServer() error = nil, want error")
		}
	})

	t.Run("rejectsNilExecutor", func(t *testing.T) {
		_, err := NewServer(ServerConfig{Router: NewRouterWithConfig(&fakeRepository{}, baseServerConfig())})
		if err == nil {
			t.Fatal("NewServer() error = nil, want error")
		}
	})

	t.Run("usesRouterBindAndPort", func(t *testing.T) {
		router := NewRouterWithConfig(&fakeRepository{}, baseServerConfig())
		srv, err := NewServer(ServerConfig{
			Router: router,
			Executor: fakeExecutor{execute: func(context.Context, Route, *http.Request) (Result, error) {
				return Result{}, nil
			}},
		})
		if err != nil {
			t.Fatalf("NewServer() error = %v", err)
		}
		if srv.Addr() != net.JoinHostPort(DefaultBind, "0") {
			t.Fatalf("Addr() = %q, want %q", srv.Addr(), net.JoinHostPort(DefaultBind, "0"))
		}
	})
}

func TestServerHandler(t *testing.T) {
	t.Run("routeNotFound_returns404", func(t *testing.T) {
		logger := &fakeEventLogger{}
		srv := mustNewTestServer(t, baseServerConfig(), fakeExecutor{execute: func(context.Context, Route, *http.Request) (Result, error) {
			return Result{}, nil
		}}, logger)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/missing", nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if got := logger.lastEvent().Event; got != "fairway_route_not_found" {
			t.Fatalf("last event = %q, want fairway_route_not_found", got)
		}
	})

	t.Run("authFailure_returns401", func(t *testing.T) {
		logger := &fakeEventLogger{}
		cfg := baseServerConfig()
		cfg.Routes = []Route{{
			Path:   "/hooks/github",
			Auth:   Auth{Type: AuthBearer, Token: "secret"},
			Action: Action{Type: ActionCronRun, Target: "job-1"},
		}}
		srv := mustNewTestServer(t, cfg, fakeExecutor{execute: func(context.Context, Route, *http.Request) (Result, error) {
			t.Fatal("executor should not be called")
			return Result{}, nil
		}}, logger)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/hooks/github", nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if got := logger.lastEvent().Event; got != "fairway_auth_failed" {
			t.Fatalf("last event = %q, want fairway_auth_failed", got)
		}
	})

	t.Run("authFailure_returns403", func(t *testing.T) {
		cfg := baseServerConfig()
		cfg.Routes = []Route{{
			Path:   "/internal/events",
			Auth:   Auth{Type: AuthLocalOnly},
			Action: Action{Type: ActionCronRun, Target: "job-1"},
		}}
		srv := mustNewTestServer(t, cfg, fakeExecutor{execute: func(context.Context, Route, *http.Request) (Result, error) {
			t.Fatal("executor should not be called")
			return Result{}, nil
		}}, &fakeEventLogger{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/internal/events", nil)
		req.RemoteAddr = "8.8.8.8:12345"
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("executorResult_passesThroughStatusBodyAndHeaders", func(t *testing.T) {
		logger := &fakeEventLogger{}
		srv := mustNewTestServer(t, baseServerConfig(), fakeExecutor{execute: func(context.Context, Route, *http.Request) (Result, error) {
			return Result{
				HTTPStatus: http.StatusAccepted,
				Body:       []byte("ok"),
				Header:     http.Header{"X-Test": []string{"1"}},
			}, nil
		}}, logger)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/hooks/github", nil)
		req.Header.Set("Authorization", "Bearer secret")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", rec.Code)
		}
		if rec.Body.String() != "ok" {
			t.Fatalf("body = %q, want ok", rec.Body.String())
		}
		if rec.Header().Get("X-Test") != "1" {
			t.Fatalf("header X-Test = %q, want 1", rec.Header().Get("X-Test"))
		}
		event := logger.lastEvent()
		if event.Event != "fairway_request_handled" {
			t.Fatalf("last event = %q, want fairway_request_handled", event.Event)
		}
		if got := event.Data["status"]; got != http.StatusAccepted {
			t.Fatalf("logged status = %#v, want %d", got, http.StatusAccepted)
		}
	})

	t.Run("executorError_returns500", func(t *testing.T) {
		logger := &fakeEventLogger{}
		srv := mustNewTestServer(t, baseServerConfig(), fakeExecutor{execute: func(context.Context, Route, *http.Request) (Result, error) {
			return Result{}, errors.New("boom")
		}}, logger)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/hooks/github", nil)
		req.Header.Set("Authorization", "Bearer secret")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if got := logger.lastEvent().Event; got != "fairway_action_failed" {
			t.Fatalf("last event = %q, want fairway_action_failed", got)
		}
	})

	t.Run("exactPathOnly_trailingSlashDistinct", func(t *testing.T) {
		srv := mustNewTestServer(t, baseServerConfig(), fakeExecutor{execute: func(context.Context, Route, *http.Request) (Result, error) {
			return Result{}, nil
		}}, &fakeEventLogger{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/hooks/github/", nil)
		req.Header.Set("Authorization", "Bearer secret")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("requestBody_reachesExecutor", func(t *testing.T) {
		srv := mustNewTestServer(t, baseServerConfig(), fakeExecutor{execute: func(_ context.Context, _ Route, req *http.Request) (Result, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if string(body) != "payload" {
				t.Fatalf("body = %q, want payload", string(body))
			}
			return Result{HTTPStatus: http.StatusOK}, nil
		}}, &fakeEventLogger{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "http://example.com/hooks/github", strings.NewReader("payload"))
		req.Header.Set("Authorization", "Bearer secret")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})
}

func TestServerLifecycle(t *testing.T) {
	listener := newFakeListener("127.0.0.1:0")
	router := NewRouterWithConfig(&fakeRepository{}, baseServerConfig())
	srv, err := NewServer(ServerConfig{
		Router: router,
		Executor: fakeExecutor{execute: func(context.Context, Route, *http.Request) (Result, error) {
			return Result{HTTPStatus: http.StatusNoContent}, nil
		}},
		Listen: func(network, address string) (net.Listener, error) {
			return listener, nil
		},
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if srv.Addr() != "127.0.0.1:0" {
		t.Fatalf("Addr() = %q, want 127.0.0.1:0", srv.Addr())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func mustNewTestServer(t *testing.T, cfg Config, exec fakeExecutor, logger EventLogger) *Server {
	t.Helper()
	router := NewRouterWithConfig(&fakeRepository{}, cfg)
	srv, err := NewServer(ServerConfig{Router: router, Executor: exec, Logger: logger})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return srv
}

func baseServerConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Port:          0,
		Bind:          DefaultBind,
		Routes: []Route{
			{
				Path:   "/hooks/github",
				Auth:   Auth{Type: AuthBearer, Token: "secret"},
				Action: Action{Type: ActionCronRun, Target: "job-1"},
			},
		},
	}
}

type fakeListener struct {
	addr   net.Addr
	closed chan struct{}
	once   sync.Once
}

func newFakeListener(addr string) *fakeListener {
	return &fakeListener{
		addr:   fakeAddr(addr),
		closed: make(chan struct{}),
	}
}

func (f *fakeListener) Accept() (net.Conn, error) {
	<-f.closed
	return nil, net.ErrClosed
}

func (f *fakeListener) Close() error {
	f.once.Do(func() {
		close(f.closed)
	})
	return nil
}

func (f *fakeListener) Addr() net.Addr {
	return f.addr
}

type fakeEventLogger struct {
	mu     sync.Mutex
	events []yardlogs.Event
}

func (f *fakeEventLogger) Write(event yardlogs.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func (f *fakeEventLogger) lastEvent() yardlogs.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) == 0 {
		return yardlogs.Event{}
	}
	return f.events[len(f.events)-1]
}

type fakeAddr string

func (f fakeAddr) Network() string { return "tcp" }
func (f fakeAddr) String() string  { return string(f) }
