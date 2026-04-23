package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

// Handler is the domain-facing surface the server delegates to. It exposes
// the agent's tool catalogue and the dispatcher behind a minimal interface
// so the mcpserver package does not depend on the full crew runtime graph.
// Call must be safe to invoke with a nil args map (the JSON-RPC client may
// omit arguments entirely for a zero-arg tool).
type Handler interface {
	Tools() []crew.Tool
	Call(ctx context.Context, name string, args map[string]any) (tools.Envelope, error)
}

// Server routes JSON-RPC frames from the client to the Handler. A single
// Server is bound to one agent for its lifetime — the mcp-serve subcommand
// constructs one per invocation.
type Server struct {
	h       Handler
	name    string
	version string
}

// NewServer returns a Server wrapping h. name/version populate ServerInfo
// in the initialize response; use the shipyard binary identity.
func NewServer(h Handler, name, version string) *Server {
	return &Server{h: h, name: name, version: version}
}

// Serve reads newline-delimited JSON frames from r and writes responses to
// w until r is exhausted or ctx is cancelled. A clean EOF is not an error;
// any other read failure is returned. The server is single-threaded: the
// next frame is not read until the previous one's response has been
// written. That matches what Claude Code expects from a stdio MCP server.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	rd := NewReader(r)
	wr := NewWriter(w)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := rd.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = writeErr(wr, nil, ErrParse, "parse error: "+err.Error())
			continue
		}
		if req.JSONRPC != "2.0" {
			if len(req.ID) > 0 {
				_ = writeErr(wr, req.ID, ErrInvalidRequest, "jsonrpc must be 2.0")
			}
			continue
		}
		s.dispatch(ctx, req, wr)
	}
}

// dispatch is split out of Serve so tests can drive a single frame. It
// never returns an error — protocol-level failures are written back to the
// client as JSON-RPC errors; handler failures become MCP tool errors.
func (s *Server) dispatch(ctx context.Context, req JSONRPCRequest, wr *Writer) {
	isNotif := len(req.ID) == 0

	switch req.Method {
	case MethodInitialize:
		if isNotif {
			return
		}
		_ = wr.Write(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: InitializeResult{
				ProtocolVersion: ProtocolVersion,
				Capabilities:    ServerCapabilities{Tools: &ToolsCapability{}},
				ServerInfo:      ServerInfo{Name: s.name, Version: s.version},
			},
		})

	case MethodInitialized:
		// Spec-mandated notification; no response.

	case MethodToolsList:
		if isNotif {
			return
		}
		ts := s.h.Tools()
		out := make([]ToolDescriptor, 0, len(ts))
		for _, t := range ts {
			out = append(out, toolToDescriptor(t))
		}
		_ = wr.Write(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  ToolsListResult{Tools: out},
		})

	case MethodToolsCall:
		if isNotif {
			return
		}
		var p ToolsCallParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = writeErr(wr, req.ID, ErrInvalidParams, "invalid params: "+err.Error())
				return
			}
		}
		if p.Name == "" {
			_ = writeErr(wr, req.ID, ErrInvalidParams, "missing tool name")
			return
		}
		var args map[string]any
		if len(p.Arguments) > 0 {
			if err := json.Unmarshal(p.Arguments, &args); err != nil {
				_ = writeErr(wr, req.ID, ErrInvalidParams, "invalid arguments: "+err.Error())
				return
			}
		}
		env, err := s.h.Call(ctx, p.Name, args)
		if err != nil {
			// Represent dispatcher-level errors (unknown tool, bad input,
			// missing driver) as MCP tool errors with isError=true. The
			// client surfaces the text to the LLM, which can then retry or
			// pick a different tool.
			_ = wr.Write(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: CallToolResult{
					Content: []TextContent{{Type: "text", Text: err.Error()}},
					IsError: true,
				},
			})
			return
		}
		_ = wr.Write(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  envelopeToResult(env),
		})

	default:
		if isNotif {
			return
		}
		_ = writeErr(wr, req.ID, ErrMethodNotFound, fmt.Sprintf("unknown method %q", req.Method))
	}
}

// writeErr is a small helper around the stringly-typed JSON-RPC error
// response. Kept package-local because callers outside server.go have no
// business fabricating protocol errors.
func writeErr(wr *Writer, id json.RawMessage, code int, msg string) error {
	return wr.Write(JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &JSONRPCError{Code: code, Message: msg},
	})
}
