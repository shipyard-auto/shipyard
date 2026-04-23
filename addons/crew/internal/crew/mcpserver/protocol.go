// Package mcpserver implements the minimum of the Model Context Protocol
// (MCP) needed to expose crew-declared tools to Claude Code as a stdio MCP
// server. The protocol is JSON-RPC 2.0 over newline-delimited JSON on stdio,
// per the MCP specification. Only four methods are supported in v1:
// initialize, notifications/initialized, tools/list, tools/call. Anything
// else returns -32601 (method not found).
//
// The package is deliberately dependency-free beyond the Go stdlib and the
// crew domain types, because this transport lives on the hot path of every
// agent turn.
package mcpserver

import "encoding/json"

// Protocol version advertised back to the client in the initialize response.
// Claude Code negotiates by picking the highest common version; this value
// matches the 2024-11-05 MCP spec that the tools/list + tools/call subset
// targets.
const ProtocolVersion = "2024-11-05"

// JSON-RPC 2.0 method names used by MCP.
const (
	MethodInitialize  = "initialize"
	MethodInitialized = "notifications/initialized"
	MethodToolsList   = "tools/list"
	MethodToolsCall   = "tools/call"
)

// JSON-RPC 2.0 error codes. The MCP spec does not add custom codes for the
// methods we implement, so we stick to the standard reservations.
const (
	ErrParse          = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
)

// JSONRPCRequest models an incoming frame. ID is json.RawMessage so the
// server can echo the exact bytes the client sent (number or string) in the
// response. An absent ID — distinguished by len(ID) == 0 — marks the message
// as a notification, which must not be answered.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse models an outgoing frame. Exactly one of Result or Error
// must be populated; the server helpers in server.go enforce that.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError is the standard error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// InitializeResult is the payload returned on successful initialize.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ServerCapabilities advertises the feature set. Only tools is set in v1.
type ServerCapabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability is intentionally empty: we advertise the ability to list
// and call tools but expose no subscription / listChanged option in v1.
type ToolsCapability struct{}

// ServerInfo identifies this server to the client, shown in Claude Code's
// inspector UI.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolDescriptor is one entry of the tools/list result. InputSchema is
// always a JSON-Schema object. OutputSchema is optional; when present it
// documents the shape returned inside CallToolResult.StructuredContent.
type ToolDescriptor struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

// ToolsListResult is the tools/list response payload.
type ToolsListResult struct {
	Tools []ToolDescriptor `json:"tools"`
}

// ToolsCallParams is the params shape for tools/call.
type ToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// TextContent is the only content block type this server emits. MCP allows
// image/audio/resource blocks; we stick to text so the envelope survives the
// transport unchanged.
type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallToolResult is the tools/call response payload. When the underlying
// crew.Envelope reports ok=false, IsError is true and Content carries the
// error message as text. On success, Content carries the full JSON envelope
// as text and StructuredContent carries the data field as JSON so clients
// can introspect it without re-parsing.
type CallToolResult struct {
	Content           []TextContent   `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}
