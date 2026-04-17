package fairway

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"time"
)

const (
	jsonRPCVersion           = "2.0"
	defaultHandshakeTimeout  = 2 * time.Second
	errCodeVersionMismatch   = -32010
	errCodeHandshakeRequired = -32011
	errCodeInternal          = -32603
	errCodeInvalidParams     = -32602
	errCodeMethodNotFound    = -32601
	errCodeInvalidRequest    = -32600
)

// SocketServerConfig configures the Fairway JSON-RPC control plane.
type SocketServerConfig struct {
	Router           *Router
	Version          string
	HandshakeTimeout time.Duration
	Status           statusProvider
	Stats            statsProvider
}

// SocketServer serves JSON-RPC 2.0 requests over an NDJSON socket stream.
type SocketServer struct {
	router           *Router
	version          string
	handshakeTimeout time.Duration
	status           statusProvider
	stats            statsProvider

	mu       sync.RWMutex
	listener net.Listener
	errCh    chan error
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type handshakeParams struct {
	Version string `json:"version"`
}

type routeDeleteParams struct {
	Path string `json:"path"`
}

type routeReplaceParams struct {
	Route Route `json:"route"`
}

// NewSocketServer constructs the Fairway JSON-RPC socket server.
func NewSocketServer(cfg SocketServerConfig) (*SocketServer, error) {
	if cfg.Router == nil {
		return nil, errors.New("router is required")
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = defaultHandshakeTimeout
	}
	return &SocketServer{
		router:           cfg.Router,
		version:          cfg.Version,
		handshakeTimeout: cfg.HandshakeTimeout,
		status:           cfg.Status,
		stats:            cfg.Stats,
		errCh:            make(chan error, 1),
	}, nil
}

// DefaultSocketPath returns the default Fairway control socket location.
func DefaultSocketPath() (string, error) {
	return defaultRuntimePath("fairway.sock")
}

// Start listens on the provided Unix socket path and serves requests asynchronously.
func (s *SocketServer) Start(path string) error {
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)

	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	go func() {
		if err := s.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
			select {
			case s.errCh <- err:
			default:
			}
		}
	}()
	return nil
}

// Serve serves JSON-RPC requests on the provided listener.
func (s *SocketServer) Serve(listener net.Listener) error {
	s.mu.Lock()
	if s.listener == nil {
		s.listener = listener
	}
	s.mu.Unlock()

	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go s.serveConn(conn)
	}
}

// Shutdown closes the listener and stops accepting new connections.
func (s *SocketServer) Shutdown() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

// Errors returns the asynchronous serve error channel.
func (s *SocketServer) Errors() <-chan error {
	return s.errCh
}

func (s *SocketServer) serveConn(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	_ = conn.SetReadDeadline(time.Now().Add(s.handshakeTimeout))
	request, rawLine, err := readRPCRequest(reader)
	if err != nil {
		return
	}
	if !isHandshakeRequest(request) {
		_ = writeRPCResponse(writer, rpcResponse{
			JSONRPC: jsonRPCVersion,
			ID:      decodeID(rawLine.ID),
			Error: &rpcError{
				Code:    errCodeHandshakeRequired,
				Message: "handshake required",
			},
		})
		return
	}

	if err := s.handleHandshake(writer, rawLine); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	for {
		request, rawLine, err = readRPCRequest(reader)
		if err != nil {
			return
		}
		response := s.dispatch(rawLine)
		if err := writeRPCResponse(writer, response); err != nil {
			return
		}
	}
}

func (s *SocketServer) handleHandshake(writer *bufio.Writer, req rpcRequest) error {
	var params handshakeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return writeRPCResponse(writer, rpcResponse{
				JSONRPC: jsonRPCVersion,
				ID:      decodeID(req.ID),
				Error: &rpcError{
					Code:    errCodeInvalidParams,
					Message: "invalid handshake params",
				},
			})
		}
	}

	if params.Version != s.version {
		return writeRPCResponse(writer, rpcResponse{
			JSONRPC: jsonRPCVersion,
			ID:      decodeID(req.ID),
			Error: &rpcError{
				Code:    errCodeVersionMismatch,
				Message: "version mismatch",
				Data: map[string]string{
					"daemon": s.version,
					"client": params.Version,
				},
			},
		})
	}

	return writeRPCResponse(writer, rpcResponse{
		JSONRPC: jsonRPCVersion,
		ID:      decodeID(req.ID),
		Result: map[string]string{
			"version": s.version,
		},
	})
}

func (s *SocketServer) dispatch(req rpcRequest) rpcResponse {
	response := rpcResponse{
		JSONRPC: jsonRPCVersion,
		ID:      decodeID(req.ID),
	}

	if req.JSONRPC != "" && req.JSONRPC != jsonRPCVersion {
		response.Error = &rpcError{Code: errCodeInvalidRequest, Message: "invalid jsonrpc version"}
		return response
	}

	switch req.Method {
	case "route.list":
		response.Result = s.router.List()
	case "route.add":
		var route Route
		if err := decodeRouteParams(req.Params, &route); err != nil {
			response.Error = &rpcError{Code: errCodeInvalidParams, Message: "invalid route params"}
			return response
		}
		if err := s.router.Add(route); err != nil {
			response.Error = &rpcError{Code: errCodeInternal, Message: err.Error()}
			return response
		}
		response.Result = route
	case "route.delete":
		var params routeDeleteParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Path == "" {
			response.Error = &rpcError{Code: errCodeInvalidParams, Message: "invalid route delete params"}
			return response
		}
		if err := s.router.Delete(params.Path); err != nil {
			response.Error = &rpcError{Code: errCodeInternal, Message: err.Error()}
			return response
		}
		response.Result = map[string]string{"path": params.Path}
	case "route.replace":
		var params routeReplaceParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			response.Error = &rpcError{Code: errCodeInvalidParams, Message: "invalid route replace params"}
			return response
		}
		if err := s.router.Replace(params.Route); err != nil {
			response.Error = &rpcError{Code: errCodeInternal, Message: err.Error()}
			return response
		}
		response.Result = params.Route
	case "status":
		response.Result = s.statusSnapshot()
	case "stats":
		response.Result = s.statsSnapshot()
	default:
		response.Error = &rpcError{Code: errCodeMethodNotFound, Message: "method not found"}
	}
	return response
}

func (s *SocketServer) statusSnapshot() StatusSnapshot {
	if s.status != nil {
		return s.status.Status()
	}

	cfg := s.router.Config()
	return StatusSnapshot{
		Version:    s.version,
		Bind:       cfg.Bind,
		Port:       cfg.Port,
		RouteCount: len(cfg.Routes),
		PID:        os.Getpid(),
	}
}

func (s *SocketServer) statsSnapshot() StatsSnapshot {
	if s.stats != nil {
		return s.stats.Stats()
	}
	return StatsSnapshot{}
}

func readRPCRequest(reader *bufio.Reader) (rpcRequest, rpcRequest, error) {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return rpcRequest{}, rpcRequest{}, err
	}
	var request rpcRequest
	if err := json.Unmarshal(bytesTrimSpace(line), &request); err != nil {
		return rpcRequest{}, rpcRequest{}, err
	}
	return request, request, nil
}

func writeRPCResponse(writer *bufio.Writer, response rpcResponse) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(payload, '\n')); err != nil {
		return err
	}
	return writer.Flush()
}

func isHandshakeRequest(req rpcRequest) bool {
	return req.Method == "handshake"
}

func decodeRouteParams(raw json.RawMessage, route *Route) error {
	if len(raw) == 0 {
		return errors.New("missing params")
	}
	if err := json.Unmarshal(raw, route); err == nil && route.Path != "" {
		return nil
	}
	var wrapped routeReplaceParams
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return err
	}
	*route = wrapped.Route
	return nil
}

func decodeID(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func bytesTrimSpace(in []byte) []byte {
	start := 0
	end := len(in)
	for start < end && (in[start] == ' ' || in[start] == '\n' || in[start] == '\r' || in[start] == '\t') {
		start++
	}
	for end > start && (in[end-1] == ' ' || in[end-1] == '\n' || in[end-1] == '\r' || in[end-1] == '\t') {
		end--
	}
	return in[start:end]
}
