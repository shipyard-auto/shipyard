package crewctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// Typed errors returned by the crew Client.
var (
	// ErrDaemonNotRunning wraps any dial failure against the daemon socket.
	ErrDaemonNotRunning = errors.New("crew: daemon is not running")
)

// ErrVersionMismatch is returned when the crew daemon reports a different
// major version than the client. The embedded strings help the CLI surface
// an actionable message ("run shipyard crew install/upgrade").
type ErrVersionMismatch struct {
	Daemon string
	Client string
}

func (e *ErrVersionMismatch) Error() string {
	return fmt.Sprintf("crew: version mismatch: daemon=%s client=%s", e.Daemon, e.Client)
}

// RPCError is the JSON-RPC 2.0 error object carried inside a response. It
// implements error so callers can use errors.As to recover the code.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// Usage mirrors the token accounting that the daemon MAY report on a run
// response. v1 of the daemon does not populate it; the field is forward
// compatible and will be zero-valued until the addon emits it.
type Usage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
}

// RunResult is the decoded payload of a successful "run" RPC call. It mirrors
// the contract produced by addons/crew/internal/crew/socket/server.go.
type RunResult struct {
	TraceID string         `json:"trace_id"`
	Text    string         `json:"text,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	Status  string         `json:"status,omitempty"`
	Usage   Usage          `json:"usage,omitempty"`
}

// wireRunResult maps the daemon's on-the-wire shape (output.text + output.data)
// into the flatter RunResult struct used by callers.
type wireRunResult struct {
	TraceID string `json:"trace_id"`
	Status  string `json:"status"`
	Output  struct {
		Text string         `json:"text"`
		Data map[string]any `json:"data"`
	} `json:"output"`
	Usage Usage `json:"usage"`
}

// Opts configures Dial.
type Opts struct {
	// SocketPath is the Unix socket path for the crew daemon.
	SocketPath string

	// Version is the client's semantic version string, sent in handshake.
	Version string

	// HandshakeTimeout overrides DefaultHandshakeTimeout when non-zero.
	HandshakeTimeout time.Duration

	// Dial overrides the low-level dial. Defaults to net.Dialer.DialContext.
	// Injected for tests.
	Dial func(ctx context.Context, path string) (net.Conn, error)
}

// Client is a JSON-RPC 2.0 client connected to a single crew daemon over a
// Unix socket. Exported methods are safe for concurrent use.
type Client struct {
	conn    net.Conn
	scanner *bufio.Scanner
	writer  *bufio.Writer

	mu     sync.Mutex
	nextID uint64

	daemonVersion string
}

// Dial opens the socket, performs the mandatory handshake and returns a Client
// ready to issue RPC calls. The returned Client owns the connection.
func Dial(ctx context.Context, opts Opts) (*Client, error) {
	dialFn := opts.Dial
	if dialFn == nil {
		dialFn = defaultDial
	}
	conn, err := dialFn(ctx, opts.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDaemonNotRunning, err)
	}

	buf := make([]byte, 1<<20)
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(buf, 1<<20)

	c := &Client{
		conn:    conn,
		scanner: scanner,
		writer:  bufio.NewWriter(conn),
	}

	timeout := opts.HandshakeTimeout
	if timeout <= 0 {
		timeout = DefaultHandshakeTimeout
	}
	if err := c.handshake(ctx, opts.Version, timeout); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// DaemonVersion returns the version reported by the daemon during handshake.
func (c *Client) DaemonVersion() string {
	return c.daemonVersion
}

// Run invokes the "run" method on the daemon. The input payload must be valid
// JSON. Timeout is forwarded as timeout_ms so the daemon can abort the runner
// independently of the socket read deadline.
func (c *Client) Run(ctx context.Context, input json.RawMessage, timeout time.Duration) (*RunResult, error) {
	params := map[string]any{
		"input":      input,
		"timeout_ms": timeout.Milliseconds(),
	}
	raw, err := c.call(ctx, "run", params, timeout)
	if err != nil {
		return nil, err
	}
	var wire wireRunResult
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("crew: run: decode result: %w", err)
	}
	return &RunResult{
		TraceID: wire.TraceID,
		Status:  wire.Status,
		Text:    wire.Output.Text,
		Data:    wire.Output.Data,
		Usage:   wire.Usage,
	}, nil
}

// handshake performs the mandatory first message on the connection.
func (c *Client) handshake(ctx context.Context, version string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("crew: handshake: set deadline: %w", err)
	}
	defer c.conn.SetDeadline(time.Time{})

	raw, err := c.rawCall("handshake", map[string]string{"version": version})
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == ErrCodeVersionMismatch {
			return versionMismatchFromErr(rpcErr)
		}
		return fmt.Errorf("crew: handshake: %w", err)
	}
	var info struct {
		Version string `json:"version"`
		Agent   string `json:"agent"`
	}
	_ = json.Unmarshal(raw, &info)
	c.daemonVersion = info.Version
	return nil
}

func versionMismatchFromErr(rpcErr *RPCError) error {
	var vd struct {
		Daemon string `json:"daemon"`
		Client string `json:"client"`
	}
	_ = json.Unmarshal(rpcErr.Data, &vd)
	return &ErrVersionMismatch{Daemon: vd.Daemon, Client: vd.Client}
}

// call performs a single request/response exchange honoring the provided
// timeout as the socket deadline.
func (c *Client) call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("crew: %s: set deadline: %w", method, err)
	}
	defer c.conn.SetDeadline(time.Time{})

	raw, err := c.rawCall(method, params)
	if err != nil {
		return nil, fmt.Errorf("crew: %s: %w", method, err)
	}
	return raw, nil
}

// rawCall writes one request and reads one response off the same connection,
// serialising concurrent callers.
func (c *Client) rawCall(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	idJSON := json.RawMessage(strconv.FormatUint(c.nextID, 10))

	var paramsJSON json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		paramsJSON = data
	}

	req := request{
		JSONRPC: JSONRPCVersion,
		ID:      idJSON,
		Method:  method,
		Params:  paramsJSON,
	}
	if err := c.writeRequest(req); err != nil {
		return nil, err
	}
	if !c.scanner.Scan() {
		scanErr := c.scanner.Err()
		if scanErr == nil {
			scanErr = io.EOF
		}
		return nil, fmt.Errorf("read: %w", scanErr)
	}
	var resp response
	if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (c *Client) writeRequest(req request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	if _, err := c.writer.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return c.writer.Flush()
}

func defaultDial(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

// request is the JSON-RPC 2.0 request envelope.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is the JSON-RPC 2.0 response envelope.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}
