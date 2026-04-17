package fairway

import "encoding/json"

// JSONRPCVersion is the version string used in all JSON-RPC 2.0 messages.
const JSONRPCVersion = "2.0"

// Standard JSON-RPC 2.0 error codes and fairway-specific custom codes
// in the -32000..-32099 reserved range.
const (
	ErrCodeParseError        = -32700
	ErrCodeInvalidRequest    = -32600
	ErrCodeMethodNotFound    = -32601
	ErrCodeInvalidParams     = -32602
	ErrCodeInternal          = -32603
	ErrCodeVersionMismatch   = -32010
	ErrCodeHandshakeRequired = -32011
	ErrCodeHandshakeTimeout  = -32012
)

// Request is a JSON-RPC 2.0 request message received from a client.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response message sent to a client.
// Exactly one of Result and Error is non-nil.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError represents the JSON-RPC 2.0 error object embedded in a Response.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
