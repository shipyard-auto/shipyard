package fairway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// ServerConfig configures the Fairway HTTP daemon.
type ServerConfig struct {
	Router            *Router
	Executor          Executor
	Logger            EventLogger
	Now               func() time.Time
	ReadHeaderTimeout time.Duration
	Listen            func(network, address string) (net.Listener, error)
}

// Server is the Fairway HTTP daemon that authenticates and dispatches inbound requests.
type Server struct {
	router   *Router
	executor Executor
	logger   EventLogger
	now      func() time.Time

	server *http.Server
	listen func(network, address string) (net.Listener, error)

	mu       sync.RWMutex
	listener net.Listener
	errCh    chan error
}

// NewServer constructs the Fairway HTTP daemon from the in-memory router config.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Router == nil {
		return nil, errors.New("router is required")
	}
	if cfg.Executor == nil {
		return nil, errors.New("executor is required")
	}
	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = noopLogger{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Listen == nil {
		cfg.Listen = net.Listen
	}

	config := cfg.Router.Config()
	addr := net.JoinHostPort(config.Bind, fmt.Sprintf("%d", config.Port))

	s := &Server{
		router:   cfg.Router,
		executor: cfg.Executor,
		logger:   cfg.Logger,
		now:      cfg.Now,
		listen:   cfg.Listen,
		errCh:    make(chan error, 1),
	}
	s.server = &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}
	return s, nil
}

// Handler returns the HTTP handler used by the Fairway daemon.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		route, ok := s.router.Match(r.URL.Path)
		if !ok {
			s.logRequest("warn", "fairway_route_not_found", "No Fairway route matched the incoming request", Route{}, r, http.StatusNotFound, Result{}, nil, start)
			http.NotFound(w, r)
			return
		}

		authenticator, err := NewAuthenticator(route.Auth)
		if err != nil {
			s.logRequest("error", "fairway_auth_config_failed", "Failed to configure Fairway route authenticator", route, r, http.StatusInternalServerError, Result{}, err, start)
			http.Error(w, "failed to configure authenticator", http.StatusInternalServerError)
			return
		}
		if err := authenticator.Verify(r); err != nil {
			if authErr, ok := IsAuth(err); ok {
				s.logRequest("warn", "fairway_auth_failed", "Fairway request authentication failed", route, r, authErr.Status, Result{}, err, start)
				http.Error(w, authErr.Reason, authErr.Status)
				return
			}
			s.logRequest("error", "fairway_auth_failed", "Fairway request authentication failed", route, r, http.StatusInternalServerError, Result{}, err, start)
			http.Error(w, "authentication failed", http.StatusInternalServerError)
			return
		}

		result, err := s.executor.Execute(r.Context(), route, r)
		if err != nil {
			s.logRequest("error", "fairway_action_failed", "Fairway route action execution failed", route, r, http.StatusInternalServerError, Result{}, err, start)
			http.Error(w, "failed to execute route", http.StatusInternalServerError)
			return
		}

		for key, values := range result.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		status := result.HTTPStatus
		if status == 0 {
			status = http.StatusOK
		}
		s.logRequest("info", "fairway_request_handled", "Fairway request handled", route, r, status, result, nil, start)
		w.WriteHeader(status)
		if len(result.Body) > 0 && r.Method != http.MethodHead {
			_, _ = w.Write(result.Body)
		}
	})
}

func (s *Server) logRequest(level, eventName, message string, route Route, req *http.Request, status int, result Result, err error, started time.Time) {
	if s.logger == nil {
		return
	}

	data := map[string]any{
		"method":     req.Method,
		"path":       req.URL.Path,
		"remoteAddr": req.RemoteAddr,
		"status":     status,
		"durationMs": s.now().Sub(started).Milliseconds(),
	}
	if route.Path != "" {
		data["routePath"] = route.Path
		data["actionType"] = route.Action.Type
	}
	if result.Truncated {
		data["truncated"] = true
	}
	if result.ExitCode != 0 {
		data["exitCode"] = result.ExitCode
	}
	if err != nil {
		data["error"] = err.Error()
	}

	_ = s.logger.Write(yardlogs.Event{
		Source:     fairwayLogSource,
		Level:      level,
		Event:      eventName,
		Message:    message,
		EntityType: "route",
		EntityID:   route.Path,
		EntityName: route.Path,
		Data:       data,
	})
}

// Start listens on the configured address and serves requests asynchronously.
func (s *Server) Start() error {
	listener, err := s.listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	go func() {
		if err := s.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case s.errCh <- err:
			default:
			}
		}
	}()

	return nil
}

// Serve serves requests on the provided listener.
func (s *Server) Serve(listener net.Listener) error {
	s.mu.Lock()
	if s.listener == nil {
		s.listener = listener
	}
	s.mu.Unlock()
	return s.server.Serve(listener)
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// Addr returns the currently bound listener address, or the configured address before Start.
func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.server.Addr
}

// Errors returns the asynchronous serve error channel.
func (s *Server) Errors() <-chan error {
	return s.errCh
}
