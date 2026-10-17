package fairwayctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Typed errors returned by Client methods.
var (
	// ErrDaemonNotRunning is returned when the fairway daemon socket is unreachable.
	ErrDaemonNotRunning = errors.New("fairway: daemon is not running")

	// ErrRouteNotFound is returned when operating on a path that does not exist.
	ErrRouteNotFound = errors.New("fairway: route not found")

	// ErrDuplicatePath is returned when adding a route whose path already exists.
	ErrDuplicatePath = errors.New("fairway: duplicate route path")
)

// ErrVersionMismatch is returned when the daemon and client versions differ.
type ErrVersionMismatch struct {
	Daemon string
	Client string
}

func (e *ErrVersionMismatch) Error() string {
	return fmt.Sprintf("fairway: version mismatch: daemon=%s client=%s", e.Daemon, e.Client)
}

// Client is a JSON-RPC 2.0 client connected to a fairway daemon over a Unix
// socket. All exported methods are safe for concurrent use.
type Client struct {
	conn    net.Conn
	scanner *bufio.Scanner
	writer  *bufio.Writer
	mu      sync.Mutex
	nextID  uint64
	version string
}

// Opts configures a Dial call.
type Opts struct {
	// SocketPath is the Unix socket path for the fairway daemon.
	SocketPath string

	// Version is the core version string sent in the handshake.
	Version string

	// Dial overrides the low-level dialer. Defaults to net.Dialer.DialContext.
	// Inject a fake for unit tests.
	Dial func(ctx context.Context, path string) (net.Conn, error)
}

// Dial connects to the fairway daemon at opts.SocketPath and performs the
// version handshake. Returns ErrDaemonNotRunning if the socket is unreachable
// or ErrVersionMismatch if the daemon reports a different version.
func Dial(ctx context.Context, opts Opts) (*Client, error) {
	dialFn := opts.Dial
	if dialFn == nil {
		dialFn = defaultDial
	}

	conn, err := dialFn(ctx, opts.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDaemonNotRunning, err)
	}

	buf := make([]byte, 1024*1024)
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(buf, 1024*1024)

	c := &Client{
		conn:    conn,
		scanner: scanner,
		writer:  bufio.NewWriter(conn),
		version: opts.Version,
	}

	if err := c.handshake(ctx, opts.Version); err != nil {
		conn.Close()
		return nil, err
	}

	return c, nil
}

// Close closes the underlying connection. Safe to call more than once.
func (c *Client) Close() error {
	return c.conn.Close()
}

// handshake sends the version handshake and validates the daemon's response.
// Uses the lesser of 2 seconds and the ctx deadline as the I/O deadline.
func (c *Client) handshake(ctx context.Context, version string) error {
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("fairway: handshake: set deadline: %w", err)
	}
	defer c.conn.SetDeadline(time.Time{})

	params, err := json.Marshal(map[string]string{"version": version})
	if err != nil {
		return fmt.Errorf("fairway: handshake: marshal: %w", err)
	}

	req := request{
		JSONRPC: jsonrpcVersion,
		ID:      json.RawMessage(`"0"`),
		Method:  "handshake",
		Params:  params,
	}

	if err := c.writeRequest(req); err != nil {
		return fmt.Errorf("fairway: handshake: %w", err)
	}

	if !c.scanner.Scan() {
		scanErr := c.scanner.Err()
		if scanErr == nil {
			scanErr = io.EOF
		}
		return fmt.Errorf("fairway: handshake: read: %w", scanErr)
	}

	var resp response
	if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
		return fmt.Errorf("fairway: handshake: decode: %w", err)
	}

	if resp.Error != nil {
		if resp.Error.Code == errCodeVersionMismatch {
			var vd struct {
				Daemon string `json:"daemon"`
				Client string `json:"client"`
			}
			_ = json.Unmarshal(resp.Error.Data, &vd)
			return &ErrVersionMismatch{Daemon: vd.Daemon, Client: vd.Client}
		}
		return fmt.Errorf("fairway: handshake: %w", resp.Error)
	}

	return nil
}

// call executes a single JSON-RPC 2.0 method call and returns the raw result.
// The mutex serialises all calls on the connection.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	idJSON := json.RawMessage(strconv.FormatUint(c.nextID, 10))

	var paramsJSON json.RawMessage
	if params != nil {
		var err error
		if paramsJSON, err = json.Marshal(params); err != nil {
			return nil, fmt.Errorf("fairway: %s: marshal params: %w", method, err)
		}
	}

	req := request{
		JSONRPC: jsonrpcVersion,
		ID:      idJSON,
		Method:  method,
		Params:  paramsJSON,
	}

	if deadline, ok := ctx.Deadline(); ok {
		if err := c.conn.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("fairway: %s: set deadline: %w", method, err)
		}
		defer c.conn.SetDeadline(time.Time{})
	}

	if err := c.writeRequest(req); err != nil {
		return nil, fmt.Errorf("fairway: %s: %w", method, err)
	}

	if !c.scanner.Scan() {
		scanErr := c.scanner.Err()
		if scanErr == nil {
			scanErr = io.EOF
		}
		return nil, fmt.Errorf("fairway: %s: read: %w", method, scanErr)
	}

	var resp response
	if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("fairway: %s: decode response: %w", method, err)
	}

	if string(resp.ID) != string(idJSON) {
		return nil, fmt.Errorf("fairway: %s: id mismatch: sent %s got %s", method, idJSON, resp.ID)
	}

	if resp.Error != nil {
		return nil, resp.Error
	}

	return resp.Result, nil
}

// writeRequest serialises req as NDJSON and flushes the buffered writer.
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

// ── RPC methods ───────────────────────────────────────────────────────────────

// RouteList returns the current list of routes from the daemon.
func (c *Client) RouteList(ctx context.Context) ([]Route, error) {
	raw, err := c.call(ctx, "route.list", nil)
	if err != nil {
		return nil, fmt.Errorf("fairway: route list: %w", err)
	}
	var routes []Route
	if err := json.Unmarshal(raw, &routes); err != nil {
		return nil, fmt.Errorf("fairway: route list: decode: %w", err)
	}
	return routes, nil
}

// RouteAdd adds a new route to the daemon. Returns ErrDuplicatePath if the
// route path already exists.
func (c *Client) RouteAdd(ctx context.Context, route Route) error {
	_, err := c.call(ctx, "route.add", map[string]Route{"route": route})
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == errCodeInvalidParams {
			if strings.Contains(rpcErr.Message, "duplicate") {
				return ErrDuplicatePath
			}
		}
		return fmt.Errorf("fairway: route add: %w", err)
	}
	return nil
}

// RouteDelete removes the route with the given path. Returns ErrRouteNotFound
// if no route with that path exists.
func (c *Client) RouteDelete(ctx context.Context, path string) error {
	_, err := c.call(ctx, "route.delete", map[string]string{"path": path})
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == errCodeInvalidParams {
			if strings.Contains(rpcErr.Message, "not found") {
				return ErrRouteNotFound
			}
		}
		return fmt.Errorf("fairway: route delete: %w", err)
	}
	return nil
}

// RouteTest fires a test request for the given route path and returns the
// HTTP response the daemon observed.
func (c *Client) RouteTest(ctx context.Context, path, method string, body []byte, headers map[string]string) (TestResult, error) {
	params := map[string]any{
		"path":    path,
		"method":  method,
		"body":    string(body),
		"headers": headers,
	}
	raw, err := c.call(ctx, "route.test", params)
	if err != nil {
		return TestResult{}, fmt.Errorf("fairway: route test: %w", err)
	}
	var result TestResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return TestResult{}, fmt.Errorf("fairway: route test: decode: %w", err)
	}
	return result, nil
}

// Status returns the daemon's current runtime status.
func (c *Client) Status(ctx context.Context) (StatusInfo, error) {
	raw, err := c.call(ctx, "status", nil)
	if err != nil {
		return StatusInfo{}, fmt.Errorf("fairway: status: %w", err)
	}
	var info StatusInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return StatusInfo{}, fmt.Errorf("fairway: status: decode: %w", err)
	}
	return info, nil
}

// Stats returns a snapshot of the daemon's request counters.
func (c *Client) Stats(ctx context.Context) (StatsSnapshot, error) {
	raw, err := c.call(ctx, "stats", nil)
	if err != nil {
		return StatsSnapshot{}, fmt.Errorf("fairway: stats: %w", err)
	}
	var snap StatsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return StatsSnapshot{}, fmt.Errorf("fairway: stats: decode: %w", err)
	}
	return snap, nil
}

func defaultDial(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

// ── Signed-route clients ─────────────────────────────────────────────────────

// ClientList returns every client (without secrets).
func (c *Client) ClientList(ctx context.Context) ([]ClientInfo, error) {
	raw, err := c.call(ctx, "client.list", nil)
	if err != nil {
		return nil, fmt.Errorf("fairway: client list: %w", err)
	}
	var out []ClientInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("fairway: client list: decode: %w", err)
	}
	return out, nil
}

// ClientAdd registers a client and returns it with its secret (shown once).
func (c *Client) ClientAdd(ctx context.Context, id string, scopes []string) (IssuedClient, error) {
	return c.issued(ctx, "client.add", map[string]any{"id": id, "scopes": scopes})
}

// ClientRotate replaces a client's secret and returns the new one.
func (c *Client) ClientRotate(ctx context.Context, id string) (IssuedClient, error) {
	return c.issued(ctx, "client.rotate", map[string]any{"id": id})
}

func (c *Client) issued(ctx context.Context, method string, params any) (IssuedClient, error) {
	raw, err := c.call(ctx, method, params)
	if err != nil {
		return IssuedClient{}, fmt.Errorf("fairway: %s: %w", method, err)
	}
	var out IssuedClient
	if err := json.Unmarshal(raw, &out); err != nil {
		return IssuedClient{}, fmt.Errorf("fairway: %s: decode: %w", method, err)
	}
	return out, nil
}

// ClientSetScopes replaces a client's scopes.
func (c *Client) ClientSetScopes(ctx context.Context, id string, scopes []string) (ClientInfo, error) {
	raw, err := c.call(ctx, "client.scopes", map[string]any{"id": id, "scopes": scopes})
	if err != nil {
		return ClientInfo{}, fmt.Errorf("fairway: client scopes: %w", err)
	}
	var out ClientInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		return ClientInfo{}, fmt.Errorf("fairway: client scopes: decode: %w", err)
	}
	return out, nil
}

// ClientDelete removes a client; its secret stops working immediately.
func (c *Client) ClientDelete(ctx context.Context, id string) error {
	if _, err := c.call(ctx, "client.delete", map[string]any{"id": id}); err != nil {
		return fmt.Errorf("fairway: client delete: %w", err)
	}
	return nil
}

// ClientSetDisabled turns a client off or back on.
func (c *Client) ClientSetDisabled(ctx context.Context, id string, disabled bool) error {
	method := "client.enable"
	if disabled {
		method = "client.disable"
	}
	if _, err := c.call(ctx, method, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("fairway: %s: %w", method, err)
	}
	return nil
}

// ── Event subscriptions ──────────────────────────────────────────────────────

// SubscriptionList returns subscriptions with delivery stats; clientID filters.
func (c *Client) SubscriptionList(ctx context.Context, clientID string) ([]Subscription, error) {
	raw, err := c.call(ctx, "subscription.list", map[string]any{"clientId": clientID})
	if err != nil {
		return nil, fmt.Errorf("fairway: subscription list: %w", err)
	}
	var out []Subscription
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("fairway: subscription list: decode: %w", err)
	}
	return out, nil
}

// SubscriptionAdd subscribes a client to a topic at a webhook URL.
func (c *Client) SubscriptionAdd(ctx context.Context, clientID, topic, url string, allowHTTP bool) (Subscription, error) {
	raw, err := c.call(ctx, "subscription.add", map[string]any{"clientId": clientID, "topic": topic, "url": url, "allowHttp": allowHTTP})
	if err != nil {
		return Subscription{}, fmt.Errorf("fairway: subscription add: %w", err)
	}
	var out Subscription
	if err := json.Unmarshal(raw, &out); err != nil {
		return Subscription{}, fmt.Errorf("fairway: subscription add: decode: %w", err)
	}
	return out, nil
}

// SubscriptionDelete removes a subscription.
func (c *Client) SubscriptionDelete(ctx context.Context, id string) error {
	if _, err := c.call(ctx, "subscription.delete", map[string]any{"id": id}); err != nil {
		return fmt.Errorf("fairway: subscription delete: %w", err)
	}
	return nil
}

// SubscriptionTest sends a shipyard.ping event to one subscription.
func (c *Client) SubscriptionTest(ctx context.Context, id string) (string, error) {
	raw, err := c.call(ctx, "subscription.test", map[string]any{"id": id})
	if err != nil {
		return "", fmt.Errorf("fairway: subscription test: %w", err)
	}
	var out struct {
		EventID string `json:"eventId"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.EventID, nil
}
