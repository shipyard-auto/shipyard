package socket

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultHandshakeTimeout is the maximum time a client has to send the
// mandatory handshake request after opening a connection.
const DefaultHandshakeTimeout = 2 * time.Second

// Handler is the signature of a JSON-RPC method handler. Returning a non-nil
// *Error causes an error response; returning nil returns the value as result.
type Handler func(ctx context.Context, params json.RawMessage) (any, *Error)

// Runner is the subset of the crew runner that the socket server depends on
// to satisfy the "run" method. Tests may provide a fake.
type Runner interface {
	Run(ctx context.Context, params RunParams) (RunResult, error)
}

// RunParams carries the decoded "run" call payload.
type RunParams struct {
	Input     map[string]any
	TimeoutMs int
}

// RunResult is returned by Runner.Run and serialized into the "run" result.
type RunResult struct {
	TraceID string
	Text    string
	Data    map[string]any
}

// Deps is the set of collaborators required by a Server.
type Deps struct {
	AgentName        string
	Version          string
	Runner           Runner
	Reload           func(context.Context) error
	OnShutdown       func()
	Now              func() time.Time
	HandshakeTimeout time.Duration
}

// Server is the Unix-socket JSON-RPC 2.0 control plane for one crew agent.
type Server struct {
	path             string
	deps             Deps
	handshakeTimeout time.Duration
	now              func() time.Time
	started          time.Time

	handlers map[string]Handler

	listenerMu sync.Mutex
	listener   net.Listener

	connsMu    sync.Mutex
	activeConn map[net.Conn]struct{}
	conns      sync.WaitGroup
	activeRuns atomic.Int64
	totalRuns  atomic.Int64

	closed atomic.Bool
}

// NewServer binds a Unix socket at path, chmods it to 0600 and registers the
// default method set. It does not start serving: callers must invoke Serve.
func NewServer(path string, deps Deps) (*Server, error) {
	if path == "" {
		return nil, errors.New("socket: path is required")
	}
	if deps.Version == "" {
		return nil, errors.New("socket: version is required")
	}
	if deps.HandshakeTimeout <= 0 {
		deps.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}

	_ = os.Remove(path)
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("socket listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = lis.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("socket chmod %s: %w", path, err)
	}

	s := &Server{
		path:             path,
		deps:             deps,
		handshakeTimeout: deps.HandshakeTimeout,
		now:              deps.Now,
		started:          deps.Now(),
		listener:         lis,
		activeConn:       make(map[net.Conn]struct{}),
	}
	s.handlers = map[string]Handler{
		"handshake": s.handleHandshake,
		"run":       s.handleRun,
		"status":    s.handleStatus,
		"reload":    s.handleReload,
		"shutdown":  s.handleShutdown,
	}
	return s, nil
}

// Addr returns the listener address, useful for tests that need to reach the
// socket through a client.
func (s *Server) Addr() net.Addr {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Path returns the filesystem path of the Unix socket.
func (s *Server) Path() string {
	return s.path
}

// Serve runs the accept loop until the listener is closed. It returns nil if
// shutdown was triggered by ctx.Done or Shutdown; any other accept error is
// returned wrapped.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.closeListener()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.closed.Load() {
				return nil
			}
			return fmt.Errorf("socket accept: %w", err)
		}
		s.conns.Add(1)
		s.trackConn(conn, true)
		go func() {
			defer s.conns.Done()
			defer s.trackConn(conn, false)
			s.handleConn(ctx, conn)
		}()
	}
}

// Shutdown closes the listener, stops accepting new connections and waits for
// the in-flight ones to finish until ctx expires. When ctx expires before all
// connections drain, Shutdown returns ctx.Err.
func (s *Server) Shutdown(ctx context.Context) error {
	s.closeListener()
	s.closeAllConns()

	done := make(chan struct{})
	go func() {
		s.conns.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) trackConn(c net.Conn, add bool) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if add {
		s.activeConn[c] = struct{}{}
	} else {
		delete(s.activeConn, c)
	}
}

func (s *Server) closeAllConns() {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	for c := range s.activeConn {
		_ = c.Close()
	}
}

func (s *Server) closeListener() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	if s.listener != nil {
		_ = s.listener.Close()
	}
	_ = os.Remove(s.path)
}

// ── Connection handling ──────────────────────────────────────────────────────

type connWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (cw *connWriter) write(resp Response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	cw.mu.Lock()
	defer cw.mu.Unlock()
	_, err = cw.w.Write(data)
	return err
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	cw := &connWriter{w: conn}

	scanner := bufio.NewScanner(conn)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, MaxMessageSize)

	// Handshake phase. Read deadline uses wall clock — the injected clock
	// drives uptime/status only.
	_ = conn.SetReadDeadline(time.Now().Add(s.handshakeTimeout))
	if !scanner.Scan() {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	var req Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		_ = cw.write(errResp(nil, ErrCodeParseError, "parse error", nil))
		return
	}
	if req.Method != "handshake" {
		_ = cw.write(errResp(req.ID, ErrCodeInvalidRequest, "handshake required as first message", nil))
		return
	}
	result, rpcErr := s.handleHandshake(ctx, req.Params)
	if rpcErr != nil {
		_ = cw.write(errRespFromError(req.ID, rpcErr))
		return
	}
	_ = cw.write(okResp(req.ID, result))

	// Dispatch loop.
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				if errors.Is(err, bufio.ErrTooLong) {
					_ = cw.write(errResp(nil, ErrCodeInvalidRequest, "message exceeds max size", nil))
				}
			}
			return
		}
		s.dispatch(ctx, scanner.Bytes(), cw)
	}
}

func (s *Server) dispatch(ctx context.Context, line []byte, cw *connWriter) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = cw.write(errResp(nil, ErrCodeParseError, "parse error", nil))
		return
	}
	if req.Jsonrpc != "" && req.Jsonrpc != JSONRPCVersion {
		_ = cw.write(errResp(req.ID, ErrCodeInvalidRequest, "invalid jsonrpc version", nil))
		return
	}
	if req.Method == "" {
		_ = cw.write(errResp(req.ID, ErrCodeInvalidRequest, "method is required", nil))
		return
	}
	h, ok := s.handlers[req.Method]
	if !ok {
		_ = cw.write(errResp(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("method %q not found", req.Method), nil))
		return
	}
	result, rpcErr := h(ctx, req.Params)
	if rpcErr != nil {
		_ = cw.write(errRespFromError(req.ID, rpcErr))
		return
	}
	_ = cw.write(okResp(req.ID, result))
}

// ── Default handlers ─────────────────────────────────────────────────────────

func (s *Server) handleHandshake(_ context.Context, raw json.RawMessage) (any, *Error) {
	var p struct {
		Version string `json:"version"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		return nil, &Error{Code: ErrCodeInvalidParams, Message: "invalid handshake params"}
	}
	if !compatibleVersion(s.deps.Version, p.Version) {
		return nil, &Error{
			Code:    ErrCodeVersionMismatch,
			Message: "version mismatch",
			Data: map[string]string{
				"daemon": s.deps.Version,
				"client": p.Version,
			},
		}
	}
	return map[string]string{
		"version": s.deps.Version,
		"agent":   s.deps.AgentName,
	}, nil
}

func (s *Server) handleRun(ctx context.Context, raw json.RawMessage) (any, *Error) {
	if s.deps.Runner == nil {
		return nil, &Error{Code: ErrCodeInternal, Message: "runner not configured"}
	}
	var p struct {
		Input     any `json:"input"`
		TimeoutMs int `json:"timeout_ms"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &Error{Code: ErrCodeInvalidParams, Message: "invalid run params"}
		}
	}
	input, err := toMap(p.Input)
	if err != nil {
		return nil, &Error{Code: ErrCodeInvalidParams, Message: err.Error()}
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if p.TimeoutMs > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(p.TimeoutMs)*time.Millisecond)
		defer cancel()
	}

	s.activeRuns.Add(1)
	defer s.activeRuns.Add(-1)
	s.totalRuns.Add(1)

	out, runErr := s.deps.Runner.Run(runCtx, RunParams{Input: input, TimeoutMs: p.TimeoutMs})
	if runErr != nil {
		return nil, &Error{
			Code:    ErrCodeAppSpecific,
			Message: runErr.Error(),
			Data:    map[string]string{"trace_id": out.TraceID},
		}
	}
	return map[string]any{
		"trace_id": out.TraceID,
		"output": map[string]any{
			"text": out.Text,
			"data": out.Data,
		},
		"status": "ok",
	}, nil
}

func (s *Server) handleStatus(_ context.Context, _ json.RawMessage) (any, *Error) {
	uptime := s.now().Sub(s.started)
	if uptime < 0 {
		uptime = 0
	}
	return map[string]any{
		"agent":          s.deps.AgentName,
		"version":        s.deps.Version,
		"uptime_seconds": int64(uptime / time.Second),
		"active_runs":    s.activeRuns.Load(),
		"total_runs":     s.totalRuns.Load(),
	}, nil
}

func (s *Server) handleReload(ctx context.Context, _ json.RawMessage) (any, *Error) {
	if s.deps.Reload == nil {
		return nil, &Error{Code: ErrCodeInternal, Message: "reload not configured"}
	}
	if err := s.deps.Reload(ctx); err != nil {
		return nil, &Error{
			Code:    ErrCodeInternal,
			Message: "reload failed",
			Data:    map[string]string{"error": err.Error()},
		}
	}
	return map[string]bool{"reloaded": true}, nil
}

func (s *Server) handleShutdown(_ context.Context, _ json.RawMessage) (any, *Error) {
	if s.deps.OnShutdown != nil {
		go s.deps.OnShutdown()
	}
	return map[string]string{"status": "shutting_down"}, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func okResp(id json.RawMessage, result any) Response {
	return Response{Jsonrpc: JSONRPCVersion, ID: id, Result: result}
}

func errResp(id json.RawMessage, code int, msg string, data any) Response {
	return Response{
		Jsonrpc: JSONRPCVersion,
		ID:      id,
		Error:   &Error{Code: code, Message: msg, Data: data},
	}
}

func errRespFromError(id json.RawMessage, e *Error) Response {
	return Response{Jsonrpc: JSONRPCVersion, ID: id, Error: e}
}

// toMap accepts any JSON-decoded value and coerces it into a map[string]any.
// A nil value produces an empty map; objects pass through; scalars/arrays are
// wrapped under key "value" so the runner always receives a map.
func toMap(v any) (map[string]any, error) {
	switch t := v.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return t, nil
	default:
		return map[string]any{"value": t}, nil
	}
}

// compatibleVersion returns true iff the daemon and client versions share the
// same semantic major. Versions that cannot be parsed as semver fall back to
// strict string equality.
func compatibleVersion(server, client string) bool {
	sM, sOK := parseMajor(server)
	cM, cOK := parseMajor(client)
	if !sOK || !cOK {
		return server == client
	}
	return sM == cM
}

func parseMajor(v string) (int, bool) {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	if s == "" {
		return 0, false
	}
	if i := strings.IndexAny(s, ".-+"); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
