package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

func TestToolToDescriptor_Minimal(t *testing.T) {
	tool := crew.Tool{Name: "noop", Protocol: crew.ToolExec, Description: "does nothing"}
	desc := toolToDescriptor(tool)

	if desc.Name != "noop" {
		t.Fatalf("name: %q", desc.Name)
	}
	if desc.Description != "does nothing" {
		t.Fatalf("description: %q", desc.Description)
	}
	if len(desc.OutputSchema) != 0 {
		t.Fatalf("outputSchema must be empty when not declared")
	}
	// inputSchema must be a valid JSON object with type=object.
	var parsed map[string]any
	if err := json.Unmarshal(desc.InputSchema, &parsed); err != nil {
		t.Fatalf("parse inputSchema: %v", err)
	}
	if parsed["type"] != "object" {
		t.Fatalf("inputSchema type: %v", parsed["type"])
	}
}

func TestToolToDescriptor_WithSchemas(t *testing.T) {
	tool := crew.Tool{
		Name:         "echo",
		Protocol:     crew.ToolExec,
		Description:  "echo back",
		InputSchema:  map[string]string{"text": "string"},
		OutputSchema: map[string]string{"echoed": "string", "bytes": "number"},
	}
	desc := toolToDescriptor(tool)

	if !strings.Contains(desc.Description, "echo back") {
		t.Fatalf("description missing original: %q", desc.Description)
	}
	if !strings.Contains(desc.Description, "Output fields:") {
		t.Fatalf("description missing output hint: %q", desc.Description)
	}
	if !strings.Contains(desc.Description, "bytes:number") ||
		!strings.Contains(desc.Description, "echoed:string") {
		t.Fatalf("description missing output fields: %q", desc.Description)
	}
	// Output summary should be sorted (bytes before echoed).
	bytesIdx := strings.Index(desc.Description, "bytes:number")
	echoedIdx := strings.Index(desc.Description, "echoed:string")
	if bytesIdx < 0 || echoedIdx < 0 || bytesIdx > echoedIdx {
		t.Fatalf("output fields not sorted in description: %q", desc.Description)
	}
	if len(desc.OutputSchema) == 0 {
		t.Fatalf("outputSchema missing")
	}
}

func TestEnvelopeToResult_Success(t *testing.T) {
	env := tools.Success(map[string]any{"echoed": "hi"})
	r := envelopeToResult(env)

	if r.IsError {
		t.Fatalf("ok envelope must not be error")
	}
	if len(r.Content) != 1 || r.Content[0].Type != "text" {
		t.Fatalf("content: %+v", r.Content)
	}
	if !strings.Contains(r.Content[0].Text, `"ok":true`) {
		t.Fatalf("text should contain envelope json: %q", r.Content[0].Text)
	}
	if len(r.StructuredContent) == 0 {
		t.Fatalf("structuredContent must be set for ok envelope with data")
	}
	var sc map[string]any
	if err := json.Unmarshal(r.StructuredContent, &sc); err != nil {
		t.Fatalf("structuredContent json: %v", err)
	}
	if sc["echoed"] != "hi" {
		t.Fatalf("structuredContent: %v", sc)
	}
}

func TestEnvelopeToResult_Failure(t *testing.T) {
	env := tools.Failure("boom", nil)
	r := envelopeToResult(env)

	if !r.IsError {
		t.Fatalf("failure envelope must be isError=true")
	}
	if len(r.StructuredContent) != 0 {
		t.Fatalf("structuredContent must be empty on failure")
	}
	if !strings.Contains(r.Content[0].Text, "boom") {
		t.Fatalf("content should contain error text: %q", r.Content[0].Text)
	}
}
