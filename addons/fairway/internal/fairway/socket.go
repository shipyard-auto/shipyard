package fairway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"
)

const defaultHandshakeTimeout = 2 * time.Second

// SocketConfig holds the dependencies for creating a SocketServer.
type SocketConfig struct {
	// Path is the Unix socket path.
	Path string

	// Router is the shared in-memory routing table.
	Router *Router

	// Server is the HTTP server used for route.test dispatches.
	// May be nil in unit tests that do not test route.test.
	Server *Server

	// Version is the daemon version string (typically app.Version).
	Version string

	// Now provides the current time. Defaults to time.Now.
	Now func() time.Time

	// HandshakeTimeout is the maximum time a client has to complete the
	// handshake after opening a connection. Defaults to 2 seconds.
	HandshakeTimeout time.Duration

	// OnInvalidate is an optional hook called after every mutation that
	// invalidates the HTTP auth cache. Defaults to Server.InvalidateAuthCache.
	// Useful for testing without a real Server.
	OnInvalidate func()

	// Stats provides live request counters for the "stats" RPC method. Optional.
	Stats *Stats
}

// SocketServer is the Unix socket JSON-RPC 2.0 control plane server.
// It accepts connections from the shipyard CLI and dispatches method calls
// to the in-memory Router.
type SocketServer struct {
	path             string
	router           *Router
	server           *Server
	version          string
	listenerMu       sync.Mutex
	listener         net.Listener
	stats            *Stats
	now              func() time.Time
	started          time.Time
	handshakeTimeout time.Duration
	onInvalidate     func()
}

// NewSocketServer creates a SocketServer from cfg.
func NewSocketServer(cfg SocketConfig) *SocketServer {
	ht := cfg.HandshakeTimeout
	if ht <= 0 {
		ht = defaultHandshakeTimeout
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	onInvalidate := cfg.OnInvalidate
	if onInvalidate == nil && cfg.Server != nil {
		onInvalidate = cfg.Server.InvalidateAuthCache
	}
	if onInvalidate == nil {
		onInvalidate = func() {}
	}
	return &SocketServer{
		path:             cfg.Path,
		router:           cfg.Router,
		server:           cfg.Server,
		version:          cfg.Version,
		stats:            cfg.Stats,
		now:              now,
		started:          now(),
		handshakeTimeout: ht,
		onInvalidate:     onInvalidate,
	}
}

// Serve removes any stale socket file, binds a Unix socket, chmods it to 0600,
// and accepts connections until ctx is cancelled. It blocks until the listener
// is closed.
func (s *SocketServer) Serve(ctx context.Context) error {
	_ = os.Remove(s.path)

	lis, err := net.Listen("unix", s.path)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", s.path, err)
	}

	s.listenerMu.Lock()
	s.listener = lis
	s.listenerMu.Unlock()

	if err := os.Chmod(s.path, 0600); err != nil {
		_ = lis.Close()
		_ = os.Remove(s.path)
		return fmt.Errorf("chmod socket: %w", err)
	}

	// LIFO: Close listener first, then remove socket file.
	defer os.Remove(s.path)
	defer lis.Close()

	// Shutdown goroutine — breaks the Accept loop when context is cancelled.
	go func() {
		<-ctx.Done()
		_ = lis.Close()
	}()

	for {
		conn, err := lis.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("accept: %w", err)
			}
		}
		go s.handleConn(ctx, conn)
	}
}

// Addr returns the net.Addr of the Unix listener after Serve is called.
func (s *SocketServer) Addr() net.Addr {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// ── Connection handler ────────────────────────────────────────────────────────

// connWriter serialises JSON-RPC responses to a connection.
// The mutex guards against concurrent writes on the same connection.
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

func (s *SocketServer) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	cw := &connWriter{w: conn}

	// 1 MB scan buffer to handle large route payloads.
	scanner := bufio.NewScanner(conn)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 1024*1024)

	// ── Handshake phase ───────────────────────────────────────────────────────
	_ = conn.SetReadDeadline(time.Now().Add(s.handshakeTimeout))

	if !scanner.Scan() {
		if isTimeoutErr(scanner.Err()) {
			_ = cw.write(errResp(nil, ErrCodeHandshakeTimeout, "handshake timeout", nil))
		}
		return
	}

	// Handshake received — clear the read deadline for the rest of the session.
	_ = conn.SetReadDeadline(time.Time{})

	var req Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		_ = cw.write(errResp(nil, ErrCodeParseError, "parse error", nil))
		return
	}

	if req.Method != "handshake" {
		_ = cw.write(errResp(req.ID, ErrCodeHandshakeRequired, "handshake required", nil))
		return
	}

	var hsParams struct {
		Version string `json:"version"`
	}
	if len(req.Params) == 0 || json.Unmarshal(req.Params, &hsParams) != nil {
		_ = cw.write(errResp(req.ID, ErrCodeInvalidParams, "invalid handshake params", nil))
		return
	}

	_ = cw.write(okResp(req.ID, map[string]string{"daemonVersion": s.version}))

	// ── Dispatch loop ─────────────────────────────────────────────────────────
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !scanner.Scan() {
			return // client closed connection or unrecoverable scan error
		}

		var dreq Request
		if err := json.Unmarshal(scanner.Bytes(), &dreq); err != nil {
			_ = cw.write(errResp(nil, ErrCodeParseError, "parse error", nil))
			continue
		}

		resp := s.dispatch(ctx, dreq)
		_ = cw.write(resp)
	}
}

// ── Dispatcher ────────────────────────────────────────────────────────────────

func (s *SocketServer) dispatch(ctx context.Context, req Request) Response {
	switch req.Method {
	case "route.list":
		routes := s.router.List()
		if routes == nil {
			routes = []Route{}
		}
		return okResp(req.ID, routes)

	case "route.add":
		var params struct {
			Route Route `json:"route"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errResp(req.ID, ErrCodeInvalidParams, "invalid params", nil)
		}
		if err := s.router.Add(params.Route); err != nil {
			return errResp(req.ID, ErrCodeInvalidParams, err.Error(), nil)
		}
		s.onInvalidate()
		return okResp(req.ID, map[string]bool{"ok": true})

	case "route.delete":
		var params struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errResp(req.ID, ErrCodeInvalidParams, "invalid params", nil)
		}
		if err := s.router.Delete(params.Path); err != nil {
			return errResp(req.ID, ErrCodeInvalidParams, err.Error(), nil)
		}
		s.onInvalidate()
		return okResp(req.ID, map[string]bool{"ok": true})

	case "route.test":
		return s.dispatchRouteTest(req)

	case "status":
		cfg := s.router.Config()
		inFlight := 0
		if s.server != nil {
			inFlight = s.server.InFlight()
		}
		return okResp(req.ID, map[string]any{
			"version":    s.version,
			"startedAt":  s.started.Format(time.RFC3339),
			"uptime":     s.now().Sub(s.started).String(),
			"port":       cfg.Port,
			"bind":       cfg.Bind,
			"routeCount": len(s.router.List()),
			"inFlight":   inFlight,
			"inflight":   inFlight,
		})

	case "stats":
		if s.server == nil || s.stats == nil {
			return okResp(req.ID, StatsSnapshot{
				ByRoute:    map[string]RouteStats{},
				ByStatus:   map[int]int64{},
				ByExitCode: map[int]int64{},
			})
		}
		snap := s.stats.Snapshot()
		return okResp(req.ID, snap)

	default:
		return errResp(req.ID, ErrCodeMethodNotFound,
			fmt.Sprintf("method %q not found", req.Method), nil)
	}
}

func (s *SocketServer) dispatchRouteTest(req Request) Response {
	var params struct {
		Path    string            `json:"path"`
		Method  string            `json:"method"`
		Body    string            `json:"body"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, ErrCodeInvalidParams, "invalid params", nil)
	}
	if params.Method == "" {
		params.Method = http.MethodPost
	}

	var bodyReader io.Reader
	if params.Body != "" {
		bodyReader = strings.NewReader(params.Body)
	}

	r := httptest.NewRequest(params.Method, params.Path, bodyReader)
	// Unix socket callers are inherently local; use loopback for auth checks.
	r.RemoteAddr = "127.0.0.1:0"
	for key, value := range params.Headers {
		r.Header.Set(key, value)
	}

	rec := httptest.NewRecorder()
	start := s.now()
	if s.server != nil {
		s.server.handler().ServeHTTP(rec, r)
	} else {
		rec.WriteHeader(http.StatusNotFound)
	}
	dur := s.now().Sub(start)

	return okResp(req.ID, map[string]any{
		"status":     rec.Code,
		"body":       rec.Body.String(),
		"durationMs": dur.Milliseconds(),
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func okResp(id json.RawMessage, result any) Response {
	return Response{JSONRPC: JSONRPCVersion, ID: id, Result: result}
}

func errResp(id json.RawMessage, code int, msg string, data any) Response {
	return Response{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg, Data: data},
	}
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
