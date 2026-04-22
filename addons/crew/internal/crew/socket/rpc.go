// Package socket implements the JSON-RPC 2.0 control plane server that the
// shipyard-crew daemon exposes over a per-agent Unix domain socket.
//
// The message frame is NDJSON: one JSON object per line terminated with "\n".
// Every connection must begin with a "handshake" call; incompatible daemon/
// client majors are rejected with error code -32010 and the connection is
// closed. After a successful handshake the connection is dispatched to the
// registered methods until the client closes or the server is shut down.
package socket

import "encoding/json"

// JSONRPCVersion is the protocol version string used in every message.
const JSONRPCVersion = "2.0"

// MaxMessageSize caps the byte length of a single framed message. Requests
// exceeding this bound are rejected with InvalidRequest.
const MaxMessageSize = 1 << 20 // 1 MiB

// Error codes defined by the JSON-RPC 2.0 spec and the crew-specific
// extensions reserved for the daemon protocol.
const (
	ErrCodeParseError      = -32700
	ErrCodeInvalidRequest  = -32600
	ErrCodeMethodNotFound  = -32601
	ErrCodeInvalidParams   = -32602
	ErrCodeInternal        = -32603
	ErrCodeVersionMismatch = -32010
	ErrCodeAppSpecific     = -32000
)

// Request is a JSON-RPC 2.0 request received from a client.
type Request struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response sent to a client. Exactly one of
// Result and Error is populated on any given response.
type Response struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the JSON-RPC 2.0 error object embedded in a Response.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
