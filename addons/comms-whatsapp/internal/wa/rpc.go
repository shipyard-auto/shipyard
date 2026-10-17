package wa

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// The daemon's control socket speaks JSON-RPC 2.0, one object per line —
// the same framing as the crew and fairway sockets. Methods:
//
//	status → Status
//	send   SendRequest → SendResult
//	limits {chat?} → LimitsReport
//
// Refusals carry their SendCode in error.data.code so clients can branch
// on it without parsing messages.

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    *rpcErrData `json:"data,omitempty"`
}

type rpcErrData struct {
	Code              SendCode `json:"code"`
	RetryAfterSeconds int      `json:"retry_after_seconds,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

// SendError converts a refusal back into the typed error, or nil when e
// is not a send refusal.
func (e *RPCError) SendError() *SendError {
	if e.Data == nil || e.Data.Code == "" {
		return nil
	}
	return &SendError{Code: e.Data.Code, Msg: e.Message, RetryAfter: time.Duration(e.Data.RetryAfterSeconds) * time.Second}
}

var sendCodeNumbers = map[SendCode]int{
	CodeInvalid:        -32602,
	CodePaused:         -32010,
	CodeNotAllowed:     -32011,
	CodeNoHistory:      -32012,
	CodeRateLimited:    -32014,
	CodeTooLong:        -32015,
	CodeNotConnected:   -32016,
	CodeUnknownChat:    -32017,
	CodeSendFailed:     -32018,
	CodeGroupsDisabled: -32019,
}

const (
	rpcMethodNotFound = -32601
	rpcParseError     = -32700
	rpcInternal       = -32603
	rpcMaxLine        = 1 << 20
	// maxSocketPath is the portable sun_path limit (104 on macOS including
	// the NUL terminator, 108 on Linux).
	maxSocketPath = 103
)

// RPCServer serves the control socket.
type RPCServer struct {
	Outbox  *Outbox
	Tracker *Tracker
	Log     *slog.Logger
}

// ListenRPC binds the socket at path with mode 0600. A leftover socket
// file is removed first: the caller holds the session lock, so no other
// daemon can be using it. The run directory is 0700, so the instant
// between listen and chmod exposes nothing to other users.
func ListenRPC(path string) (net.Listener, error) {
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("rpc: socket path %s is %d bytes; unix sockets allow %d — use a shorter SHIPYARD_HOME", path, len(path), maxSocketPath)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("rpc: remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("rpc: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("rpc: chmod %s: %w", path, err)
	}
	return ln, nil
}

// Serve accepts connections until ctx is done, then closes ln and every
// open connection (an idle client must not hold shutdown hostage) and
// waits for the handlers. A send in progress finishes even if its client
// hangs up — requests run on the server's ctx, not the connection's — so a
// message is never left sent but unrecorded because a caller went away.
func (s *RPCServer) Serve(ctx context.Context, ln net.Listener) {
	var wg sync.WaitGroup
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleConn(ctx, conn)
		}()
	}
	wg.Wait()
}

func (s *RPCServer) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), rpcMaxLine)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		var req rpcRequest
		resp := rpcResponse{JSONRPC: "2.0"}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			resp.Error = &RPCError{Code: rpcParseError, Message: "parse error: " + err.Error()}
		} else {
			resp.ID = req.ID
			resp.Result, resp.Error = s.dispatch(ctx, req)
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func (s *RPCServer) dispatch(ctx context.Context, req rpcRequest) (any, *RPCError) {
	switch req.Method {
	case "status":
		return s.Tracker.Snapshot(), nil
	case "limits":
		var p struct {
			Chat string `json:"chat"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, &RPCError{Code: sendCodeNumbers[CodeInvalid], Message: "invalid params: " + err.Error(), Data: &rpcErrData{Code: CodeInvalid}}
			}
		}
		rep, err := s.limits(ctx, p.Chat)
		if err != nil {
			var se *SendError
			if errors.As(err, &se) {
				return nil, &RPCError{Code: sendCodeNumbers[se.Code], Message: se.Msg, Data: &rpcErrData{Code: se.Code}}
			}
			return nil, &RPCError{Code: rpcInternal, Message: err.Error()}
		}
		return rep, nil
	case "send":
		var sr SendRequest
		if err := json.Unmarshal(req.Params, &sr); err != nil {
			return nil, &RPCError{Code: sendCodeNumbers[CodeInvalid], Message: "invalid params: " + err.Error(),
				Data: &rpcErrData{Code: CodeInvalid}}
		}
		res, err := s.Outbox.Send(ctx, sr)
		if err == nil {
			return res, nil
		}
		var se *SendError
		if errors.As(err, &se) {
			return nil, &RPCError{Code: sendCodeNumbers[se.Code], Message: se.Msg,
				Data: &rpcErrData{Code: se.Code, RetryAfterSeconds: int(se.RetryAfter.Seconds())}}
		}
		s.Log.Error(yardlogs.EventWARPCError, slog.String(yardlogs.KeyMessage, "send failed internally"),
			slog.String(yardlogs.KeyError, err.Error()))
		return nil, &RPCError{Code: rpcInternal, Message: err.Error()}
	default:
		return nil, &RPCError{Code: rpcMethodNotFound, Message: "unknown method " + req.Method}
	}
}

// limits builds the LimitsReport for an optional chat (name, phone or JID).
func (s *RPCServer) limits(ctx context.Context, chatQuery string) (LimitsReport, error) {
	cfg, err := s.Outbox.Config()
	if err != nil {
		return LimitsReport{}, err
	}
	var chat *archive.Chat
	if chatQuery != "" {
		c, err := ResolveChat(ctx, s.Outbox.Archive, chatQuery)
		if err != nil {
			return LimitsReport{}, refuse(CodeUnknownChat, "%v", err)
		}
		chat = &c
	}
	return BuildLimitsReport(ctx, s.Outbox.Archive, cfg, s.Outbox.Now(), chat)
}

// ErrDaemonNotRunning is returned by CallRPC when nothing listens on the
// socket.
var ErrDaemonNotRunning = errors.New("the WhatsApp daemon is not running (see 'status'; start it with 'serve' or its service)")

// RPCTimeout bounds one call: typing delay (≤8s) plus a slow send.
const RPCTimeout = 60 * time.Second

// CallRPC performs one request against the daemon socket at path and
// decodes the result into out. Refusals come back as *RPCError.
func CallRPC(ctx context.Context, path, method string, params, out any) error {
	// No socket file means no daemon; checking first also avoids an opaque
	// EINVAL from dial when the path is too long to ever be bound.
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return ErrDaemonNotRunning
	}
	ctx, cancel := context.WithTimeout(ctx, RPCTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return ErrDaemonNotRunning
		}
		return fmt.Errorf("rpc: dial: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("rpc: write: %w", err)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), rpcMaxLine)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return fmt.Errorf("rpc: read: %w", err)
		}
		return errors.New("rpc: daemon closed the connection without answering")
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return fmt.Errorf("rpc: decode: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("rpc: decode result: %w", err)
		}
	}
	return nil
}
