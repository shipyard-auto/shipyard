package mcpfilter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/mcpserver"
)

// pumpDownstream reads frames coming from claude. Most pass through to
// upstream as-is. tools/call is intercepted: blocked names are answered
// directly to claude with a JSON-RPC error and never forwarded. tools/list
// requests are forwarded but their IDs are recorded so the upstream pump
// knows to filter the response.
func pumpDownstream(
	in *mcpserver.Reader,
	upOut *mcpserver.Writer,
	downOut *mcpserver.Writer,
	pending *pendingMap,
	allowed map[string]struct{},
	allowAll bool,
) error {
	for {
		raw, err := in.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("downstream read: %w", err)
		}

		var req mcpserver.JSONRPCRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			// Unparseable frame: best-effort relay so the upstream can
			// surface its own error. Most clients send valid JSON.
			if err := upOut.WriteRaw(raw); err != nil {
				return fmt.Errorf("upstream write: %w", err)
			}
			continue
		}

		switch req.Method {
		case mcpserver.MethodToolsCall:
			name, ok := extractToolName(req.Params)
			if !ok {
				// Malformed params: let upstream answer.
				pending.put(req.ID, req.Method)
				if err := upOut.WriteRaw(raw); err != nil {
					return fmt.Errorf("upstream write: %w", err)
				}
				continue
			}
			if !allowAll {
				if _, allowedTool := allowed[name]; !allowedTool {
					// Blocked — answer downstream directly.
					if err := writeBlockedToolError(downOut, req.ID, name); err != nil {
						return fmt.Errorf("downstream write blocked: %w", err)
					}
					continue
				}
			}
			// Allowed: forward verbatim.
			if err := upOut.WriteRaw(raw); err != nil {
				return fmt.Errorf("upstream write: %w", err)
			}

		case mcpserver.MethodToolsList:
			pending.put(req.ID, req.Method)
			if err := upOut.WriteRaw(raw); err != nil {
				return fmt.Errorf("upstream write: %w", err)
			}

		default:
			if err := upOut.WriteRaw(raw); err != nil {
				return fmt.Errorf("upstream write: %w", err)
			}
		}
	}
}

// pumpUpstream reads frames coming from the upstream MCP server. Responses
// whose ID was recorded as a tools/list request get their tools array
// filtered. Everything else passes through unchanged.
func pumpUpstream(
	in *mcpserver.Reader,
	out *mcpserver.Writer,
	pending *pendingMap,
	allowed map[string]struct{},
	allowAll bool,
) error {
	for {
		raw, err := in.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("upstream read: %w", err)
		}

		// Peek at the id field so we can decide whether to filter.
		var head struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(raw, &head)

		method := pending.takeMethod(head.ID)
		if method != mcpserver.MethodToolsList {
			if err := out.WriteRaw(raw); err != nil {
				return fmt.Errorf("downstream write: %w", err)
			}
			continue
		}

		// Filter the tools array.
		filtered, err := filterToolsListResponse(raw, allowed, allowAll)
		if err != nil {
			// Best-effort forward of original on parse failure — keeps the
			// channel alive and surfaces the upstream's output as-is.
			if err := out.WriteRaw(raw); err != nil {
				return fmt.Errorf("downstream write: %w", err)
			}
			continue
		}
		if err := out.WriteRaw(filtered); err != nil {
			return fmt.Errorf("downstream write: %w", err)
		}
	}
}

// extractToolName pulls params.name from a tools/call frame. Returns
// (name, true) on success or ("", false) when params is missing/malformed.
func extractToolName(params json.RawMessage) (string, bool) {
	if len(params) == 0 {
		return "", false
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", false
	}
	if p.Name == "" {
		return "", false
	}
	return p.Name, true
}

// writeBlockedToolError responds to a tools/call request that asked for a
// tool not in the whitelist. The error code mirrors the MCP "method not
// found" convention so clients treat it as a permanent rejection rather
// than a transient failure.
func writeBlockedToolError(w *mcpserver.Writer, id json.RawMessage, name string) error {
	resp := mcpserver.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &mcpserver.JSONRPCError{
			Code:    mcpserver.ErrMethodNotFound,
			Message: fmt.Sprintf("tool %q not in allowed list for this agent", name),
		},
	}
	return w.Write(resp)
}

// filterToolsListResponse decodes a tools/list response, keeps only the
// allowed entries, and re-marshals the envelope. Error responses (frames
// with an "error" field) pass through unchanged.
func filterToolsListResponse(raw []byte, allowed map[string]struct{}, allowAll bool) ([]byte, error) {
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  *struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result,omitempty"`
		Error json.RawMessage `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	// Error response or missing result: don't touch.
	if resp.Result == nil || len(resp.Error) > 0 {
		return raw, nil
	}
	if allowAll {
		return raw, nil
	}
	kept := make([]json.RawMessage, 0, len(resp.Result.Tools))
	for _, t := range resp.Result.Tools {
		var head struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(t, &head); err != nil {
			continue
		}
		if _, ok := allowed[head.Name]; ok {
			kept = append(kept, t)
		}
	}
	resp.Result.Tools = kept
	return json.Marshal(resp)
}
