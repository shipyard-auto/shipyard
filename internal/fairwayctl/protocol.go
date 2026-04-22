// Package fairwayctl provides the JSON-RPC 2.0 client used by shipyard CLI
// commands to communicate with a running shipyard-fairway daemon via Unix socket.
package fairwayctl

import (
	"encoding/json"
	"fmt"
	"time"
)

const jsonrpcVersion = "2.0"

// JSON-RPC 2.0 standard codes and fairway-specific codes (-32000..-32099 range).
const (
	errCodeMethodNotFound  = -32601
	errCodeInvalidParams   = -32602
	errCodeVersionMismatch = -32010
)

// request is a JSON-RPC 2.0 request message.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is a JSON-RPC 2.0 response message decoded from the daemon.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC 2.0 error object carried inside a response.
// It implements the error interface so it can be used directly with errors.As.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// ── Domain types — mirror of addons/fairway/internal/fairway/model.go ────────
//
// These types are intentionally duplicated. The fairway addon and the core must
// not import each other; the protocol (JSON over NDJSON) is the only bridge.
// If model.go in the daemon changes, update this file accordingly.

// AuthType enumerates authentication strategies.
type AuthType string

const (
	AuthBearer    AuthType = "bearer"
	AuthToken     AuthType = "token"
	AuthLocalOnly AuthType = "local-only"
)

// ActionType enumerates dispatchable action types.
type ActionType string

const (
	ActionCronRun        ActionType = "cron.run"
	ActionCronEnable     ActionType = "cron.enable"
	ActionCronDisable    ActionType = "cron.disable"
	ActionServiceStart   ActionType = "service.start"
	ActionServiceStop    ActionType = "service.stop"
	ActionServiceRestart ActionType = "service.restart"
	ActionMessageSend    ActionType = "message.send"
	ActionTelegramHandle ActionType = "telegram.handle"
	ActionHTTPForward    ActionType = "http.forward"
	ActionCrewRun        ActionType = "crew.run"
)

// Auth holds the authentication configuration for a Route.
type Auth struct {
	Type   AuthType `json:"type"`
	Token  string   `json:"token,omitempty"`
	Value  string   `json:"value,omitempty"`
	Header string   `json:"header,omitempty"`
	Query  string   `json:"query,omitempty"`
}

// Action describes the operation executed when a Route is triggered.
type Action struct {
	Type     ActionType        `json:"type"`
	Target   string            `json:"target,omitempty"`
	Provider string            `json:"provider,omitempty"`
	URL      string            `json:"url,omitempty"`
	Method   string            `json:"method,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// Route describes a single HTTP endpoint and its corresponding action.
type Route struct {
	Path    string        `json:"path"`
	Auth    Auth          `json:"auth"`
	Action  Action        `json:"action"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// ── Result types returned by RPC methods ─────────────────────────────────────

// StatusInfo is the payload returned by the "status" RPC method.
type StatusInfo struct {
	Version    string `json:"version"`
	StartedAt  string `json:"startedAt"`
	Uptime     string `json:"uptime"`
	Port       int    `json:"port"`
	Bind       string `json:"bind"`
	RouteCount int    `json:"routeCount"`
	InFlight   int    `json:"inFlight"`
}

// RouteStats is a per-route counter snapshot inside StatsSnapshot.
// Field names are capitalized to match the daemon's untagged struct fields.
type RouteStats struct {
	Count    int64     `json:"Count"`
	ErrCount int64     `json:"ErrCount"`
	LastAt   time.Time `json:"LastAt"`
}

// StatsSnapshot is the payload returned by the "stats" RPC method.
// Field names are capitalized to match the daemon's untagged struct fields.
type StatsSnapshot struct {
	Total      int64                 `json:"Total"`
	ByRoute    map[string]RouteStats `json:"ByRoute"`
	ByStatus   map[int]int64         `json:"ByStatus"`
	ByExitCode map[int]int64         `json:"ByExitCode"`
	StartedAt  time.Time             `json:"StartedAt"`
}

// TestResult is the payload returned by the "route.test" RPC method.
type TestResult struct {
	Status     int    `json:"status"`
	Body       string `json:"body"`
	DurationMs int64  `json:"durationMs"`
}
