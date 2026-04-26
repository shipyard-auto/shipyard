package fairway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/logs/trace"
)

const (
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 10 * time.Second
	httpWriteTimeout      = MaxRouteTimeout + 30*time.Second
	httpShutdownTimeout   = 10 * time.Second
)

// ServerConfig holds the dependencies for creating an HTTP server.
type ServerConfig struct {
	// Router is the in-memory routing table.
	Router *Router

	// Executor dispatches route actions.
	Executor Executor

	// Logger is used for structured daemon logging.
	Logger *slog.Logger

	// EventLogger emits structured request events to the unified shipyard log
	// store (schema v2). When set, an http_request line is written by the
	// middleware for every completed request and an async_dispatch_finished
	// line is written when an async route's detached goroutine finishes.
	// Optional; when nil the middleware still wraps the mux to inject trace
	// ids but emits no log lines.
	EventLogger *slog.Logger

	// Stats tracks per-route request counters. Optional.
	Stats *Stats
}

// Server is the HTTP server for the fairway daemon. It matches incoming
// requests against the routing table, authenticates them, and dispatches
// the configured action via the Executor.
type Server struct {
	router      *Router
	executor    Executor
	logger      *slog.Logger
	eventLogger *slog.Logger
	stats       *Stats
	httpSrv     *http.Server
	bind        string
	port        int
	addr        string     // real address after Listen (set in Serve)
	addrMu      sync.Mutex // protects addr

	authCacheMu sync.RWMutex
	authCache   map[string]Authenticator

	// asyncWG tracks goroutines spawned for async routes so a graceful
	// shutdown can wait for them to complete before returning.
	asyncWG sync.WaitGroup
}

// NewServer creates an HTTP server from cfg.
func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		router:      cfg.Router,
		executor:    cfg.Executor,
		logger:      cfg.Logger,
		eventLogger: cfg.EventLogger,
		stats:       cfg.Stats,
		authCache:   make(map[string]Authenticator),
	}

	if s.logger == nil {
		s.logger = slog.Default()
	}

	rc := cfg.Router.Config()
	s.bind = rc.Bind
	s.port = rc.Port

	mux := http.NewServeMux()
	mux.HandleFunc("/_health", s.handleHealth)
	mux.HandleFunc("/", s.handleRouted)

	// Always wrap with the trace-injecting middleware so every request gets
	// a trace id propagated via context and echoed in the response header.
	// When no EventLogger is configured we still wrap, falling back to a nop
	// logger so the structured log line is silently dropped.
	mwLogger := s.eventLogger
	if mwLogger == nil {
		mwLogger = slog.New(yardlogs.NopHandler())
	}
	handler := yardlogs.Middleware(mwLogger)(mux)

	s.httpSrv = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
	}

	return s
}

// Serve binds to the configured address, starts serving, and blocks until
// ctx is cancelled. It performs a graceful shutdown allowing in-flight requests
// up to httpShutdownTimeout to complete.
func (s *Server) Serve(ctx context.Context) error {
	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", s.bind, s.port))
	if err != nil {
		return fmt.Errorf("listen %s:%d: %w", s.bind, s.port, err)
	}
	s.addrMu.Lock()
	s.addr = lis.Addr().String()
	s.addrMu.Unlock()

	// Graceful shutdown goroutine. Waits for:
	//   1. active HTTP connections to drain (httpSrv.Shutdown);
	//   2. in-flight async goroutines to finish, up to the remaining budget.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutCtx)

		done := make(chan struct{})
		go func() {
			s.asyncWG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-shutCtx.Done():
		}
	}()

	err = s.httpSrv.Serve(lis)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Addr returns the real address the server is listening on.
// It is populated after Serve is called.
func (s *Server) Addr() string {
	s.addrMu.Lock()
	defer s.addrMu.Unlock()
	return s.addr
}

// InFlight returns the number of currently running pooled subprocess actions.
func (s *Server) InFlight() int {
	if reporter, ok := s.executor.(InFlightReporter); ok {
		return reporter.InFlight()
	}
	return 0
}

// handler returns the internal http.Handler. Used by the socket server for
// route.test without binding a real TCP port.
func (s *Server) handler() http.Handler {
	return s.httpSrv.Handler
}

// ServerHandlerForTest returns the internal http.Handler of the server.
// It exists solely to allow handler-level tests without binding a real TCP port.
// Must not be used in production code.
func ServerHandlerForTest(s *Server) http.Handler {
	return s.httpSrv.Handler
}

// InvalidateAuthCache clears the authenticator cache.
// Must be called after a route is added, deleted, or replaced.
func (s *Server) InvalidateAuthCache() {
	s.authCacheMu.Lock()
	defer s.authCacheMu.Unlock()
	s.authCache = make(map[string]Authenticator)
}

// handleHealth responds to /_health with 200 OK. No auth required.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleRouted matches the request path against the routing table, authenticates,
// and dispatches the action.
func (s *Server) handleRouted(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	route, ok := s.router.Match(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Wrap the ResponseWriter so we can capture the status code for logging.
	sc := &statusCapture{ResponseWriter: w, status: http.StatusOK}

	auth, err := s.getOrCreateAuth(route)
	if err != nil {
		http.Error(sc, `{"error":"internal auth error"}`, http.StatusInternalServerError)
		s.observeRequest(requestObservation{
			Route:      route,
			Method:     r.Method,
			Status:     sc.status,
			Duration:   time.Since(start),
			RemoteAddr: r.RemoteAddr,
			AuthType:   string(route.Auth.Type),
			AuthResult: "internal-error",
			ExitCode:   -1,
		})
		return
	}

	authType := string(route.Auth.Type)

	if err := auth.Verify(r); err != nil {
		if ae, ok := IsAuthError(err); ok {
			sc.Header().Set("Content-Type", "application/json")
			sc.WriteHeader(ae.Status)
			body, _ := json.Marshal(map[string]string{"error": ae.Reason})
			_, _ = sc.Write(body)
			s.observeRequest(requestObservation{
				Route:      route,
				Method:     r.Method,
				Status:     sc.status,
				Duration:   time.Since(start),
				RemoteAddr: r.RemoteAddr,
				AuthType:   authType,
				AuthResult: "denied",
				ExitCode:   -1,
			})
			return
		}
		http.Error(sc, `{"error":"authentication failed"}`, http.StatusInternalServerError)
		s.observeRequest(requestObservation{
			Route:      route,
			Method:     r.Method,
			Status:     sc.status,
			Duration:   time.Since(start),
			RemoteAddr: r.RemoteAddr,
			AuthType:   authType,
			AuthResult: "internal-error",
			ExitCode:   -1,
		})
		return
	}

	// Read and cap request body before passing to executor.
	var bodyBytes []byte
	if r.Body != nil {
		limited := http.MaxBytesReader(sc, r.Body, MaxSubprocessOutput)
		readBytes, readErr := io.ReadAll(limited)
		if readErr != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(readErr, &maxBytesErr) {
				http.Error(sc, "request body too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(sc, "error reading request body", http.StatusInternalServerError)
			}
			s.observeRequest(requestObservation{
				Route:      route,
				Method:     r.Method,
				Status:     sc.status,
				Duration:   time.Since(start),
				RemoteAddr: r.RemoteAddr,
				AuthType:   authType,
				AuthResult: "ok",
				ExitCode:   -1,
			})
			return
		}
		bodyBytes = readBytes
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	if route.Async {
		s.dispatchAsync(sc, r, route, authType, start, bodyBytes)
		return
	}

	result, err := s.executor.Execute(r.Context(), route, r)
	if err != nil {
		http.Error(sc, `{"error":"executor error"}`, http.StatusInternalServerError)
		s.observeRequest(requestObservation{
			Route:      route,
			Method:     r.Method,
			Status:     sc.status,
			Duration:   time.Since(start),
			RemoteAddr: r.RemoteAddr,
			AuthType:   authType,
			AuthResult: "ok",
			ExitCode:   -1,
		})
		return
	}

	// For http.forward, proxy response headers to the caller.
	if result.Header != nil {
		for k, vs := range result.Header {
			for _, v := range vs {
				sc.Header().Add(k, v)
			}
		}
	}

	sc.WriteHeader(result.HTTPStatus)
	_, _ = sc.Write(result.Body)

	dur := time.Since(start)
	s.observeRequest(requestObservation{
		Route:      route,
		Method:     r.Method,
		Status:     sc.status,
		Duration:   dur,
		RemoteAddr: r.RemoteAddr,
		AuthType:   authType,
		AuthResult: "ok",
		ExitCode:   result.ExitCode,
		Truncated:  result.Truncated,
	})
}

// dispatchAsync handles routes flagged with Async=true. It responds
// 202 Accepted immediately (with an X-Trace-Id header for correlation)
// and runs the action in a detached goroutine so the caller is not blocked
// by the action's latency. The observer sees a single record per request,
// reflecting the real ExitCode and duration of the action, but with
// Status=202 (what the client actually received).
func (s *Server) dispatchAsync(sc *statusCapture, r *http.Request, route Route, authType string, start time.Time, bodyBytes []byte) {
	// Prefer the trace id injected by the logging middleware (canonical
	// path). Fall back to generating one only when the middleware was
	// somehow bypassed, so async paths still produce correlatable logs.
	traceID := trace.ID(r.Context())
	if traceID == "" {
		traceID = newTraceID()
	}

	// Determine the effective timeout for the detached action.
	timeout := route.Timeout
	if timeout == 0 {
		timeout = DefaultActionTimeout
	}

	// Clone the request against a fresh, detached context so the work
	// survives the client disconnect and the ServeHTTP return. We propagate
	// the trace id explicitly because the new context does not inherit from
	// r.Context().
	asyncCtx, cancel := context.WithTimeout(context.Background(), timeout)
	asyncCtx = trace.WithID(asyncCtx, traceID)
	reqCopy := r.Clone(asyncCtx)
	if bodyBytes != nil {
		reqCopy.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	} else {
		reqCopy.Body = http.NoBody
	}

	// Captura snapshot dos campos que serão usados após ServeHTTP retornar.
	method := r.Method
	remoteAddr := r.RemoteAddr

	sc.Header().Set("X-Trace-Id", traceID)
	sc.Header().Set("Content-Type", "application/json")
	sc.WriteHeader(http.StatusAccepted)
	_, _ = sc.Write([]byte(fmt.Sprintf(`{"status":"accepted","trace_id":%q}`, traceID)))

	s.asyncWG.Add(1)
	go func() {
		defer s.asyncWG.Done()
		defer cancel()

		result, execErr := s.executor.Execute(asyncCtx, route, reqCopy)

		obs := requestObservation{
			Route:      route,
			Method:     method,
			Status:     http.StatusAccepted,
			Duration:   time.Since(start),
			RemoteAddr: remoteAddr,
			AuthType:   authType,
			AuthResult: "ok",
			TraceID:    traceID,
		}
		if execErr != nil {
			obs.ExitCode = -1
		} else {
			obs.ExitCode = result.ExitCode
			obs.Truncated = result.Truncated
		}
		s.observeRequest(obs)
		s.logAsyncDispatch(asyncCtx, obs, execErr)
	}()
}

// logAsyncDispatch emits a structured async_dispatch_finished line via the
// event logger so async routes can be correlated with the synchronous 202
// already logged by the middleware (same trace_id).
func (s *Server) logAsyncDispatch(ctx context.Context, obs requestObservation, execErr error) {
	if s.eventLogger == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String(yardlogs.KeyHTTPMethod, obs.Method),
		slog.String(yardlogs.KeyHTTPPath, obs.Route.Path),
		slog.Int(yardlogs.KeyHTTPStatus, obs.Status),
		slog.Int64(yardlogs.KeyDurationMs, obs.Duration.Milliseconds()),
		slog.String(yardlogs.KeyHTTPRemoteAddr, obs.RemoteAddr),
		slog.String(yardlogs.KeyRouteAction, string(obs.Route.Action.Type)),
		slog.String(yardlogs.KeyRouteTarget, obs.Route.Action.Target),
		slog.Int(yardlogs.KeyRouteExitCode, obs.ExitCode),
		slog.String(yardlogs.KeyAuthType, obs.AuthType),
		slog.String(yardlogs.KeyAuthResult, obs.AuthResult),
	}
	level := slog.LevelInfo
	if execErr != nil {
		level = slog.LevelError
		attrs = append(attrs,
			slog.String(yardlogs.KeyError, execErr.Error()),
			slog.String(yardlogs.KeyErrorKind, fmt.Sprintf("%T", execErr)),
		)
	}
	s.eventLogger.LogAttrs(ctx, level, yardlogs.EventAsyncDispatch, attrs...)
}

// newTraceID returns a random 16-char hex identifier used to correlate
// async requests across the ack response and the final log entry.
func newTraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type requestObservation struct {
	Route      Route
	Method     string
	Status     int
	Duration   time.Duration
	RemoteAddr string
	AuthType   string
	AuthResult string
	ExitCode   int
	Truncated  bool
	TraceID    string
}

// observeRequest records a completed request in stats. Structured logging
// is handled by the unified middleware (yardlogs.Middleware) — this hook
// only feeds the in-memory counter.
func (s *Server) observeRequest(obs requestObservation) {
	if s.stats != nil {
		s.stats.ObserveResult(obs.Route.Path, obs.Status, obs.ExitCode, obs.Duration)
	}
}

// statusCapture wraps an http.ResponseWriter to record the status code written.
type statusCapture struct {
	http.ResponseWriter
	status  int
	written bool
}

func (sc *statusCapture) WriteHeader(code int) {
	if !sc.written {
		sc.status = code
		sc.written = true
	}
	sc.ResponseWriter.WriteHeader(code)
}

func (sc *statusCapture) Write(b []byte) (int, error) {
	if !sc.written {
		sc.WriteHeader(http.StatusOK)
	}
	return sc.ResponseWriter.Write(b)
}

// getOrCreateAuth returns a cached Authenticator for the route path,
// creating one if necessary.
func (s *Server) getOrCreateAuth(route Route) (Authenticator, error) {
	s.authCacheMu.RLock()
	if auth, ok := s.authCache[route.Path]; ok {
		s.authCacheMu.RUnlock()
		return auth, nil
	}
	s.authCacheMu.RUnlock()

	s.authCacheMu.Lock()
	defer s.authCacheMu.Unlock()
	// Double-check after acquiring write lock.
	if auth, ok := s.authCache[route.Path]; ok {
		return auth, nil
	}
	auth, err := NewAuthenticator(route.Auth)
	if err != nil {
		return nil, err
	}
	s.authCache[route.Path] = auth
	return auth, nil
}
