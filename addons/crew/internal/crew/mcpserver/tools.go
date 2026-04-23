package mcpserver

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

// toolToDescriptor translates a crew.Tool into the MCP ToolDescriptor shape.
// InputSchema goes into inputSchema verbatim (mapped through the shared
// tools.BuildJSONSchema helper). OutputSchema, when declared, is emitted
// both as a proper outputSchema field and summarised in the description so
// that clients which don't yet consume outputSchema still see the contract.
func toolToDescriptor(t crew.Tool) ToolDescriptor {
	inSchema := tools.BuildJSONSchema(t.InputSchema)
	inRaw, _ := json.Marshal(inSchema)
	desc := ToolDescriptor{
		Name:        t.Name,
		Description: buildDescription(t),
		InputSchema: inRaw,
	}
	if len(t.OutputSchema) > 0 {
		outSchema := tools.BuildJSONSchema(t.OutputSchema)
		outRaw, _ := json.Marshal(outSchema)
		desc.OutputSchema = outRaw
	}
	return desc
}

// buildDescription concatenates the tool's human description with a compact
// "Output fields: k1:t1, k2:t2" summary when OutputSchema is declared. The
// summary is sorted by field name so the output is stable across runs.
func buildDescription(t crew.Tool) string {
	if len(t.OutputSchema) == 0 {
		return t.Description
	}
	var b strings.Builder
	if t.Description != "" {
		b.WriteString(t.Description)
		b.WriteString("\n\n")
	}
	b.WriteString("Output fields: ")
	names := make([]string, 0, len(t.OutputSchema))
	for k := range t.OutputSchema {
		names = append(names, k)
	}
	sort.Strings(names)
	for i, k := range names {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s:%s", k, t.OutputSchema[k])
	}
	return b.String()
}

// envelopeToResult translates the crew tool envelope contract to the MCP
// tools/call result shape. The full envelope JSON is always present as a
// text block so humans and fallback clients can read it; StructuredContent
// carries the decoded data only when the call succeeded and produced a
// payload, which is the happy path clients should consume.
func envelopeToResult(env tools.Envelope) CallToolResult {
	body, _ := json.Marshal(env)
	result := CallToolResult{
		Content: []TextContent{{Type: "text", Text: string(body)}},
		IsError: !env.Ok,
	}
	if env.Ok && len(env.Data) > 0 {
		result.StructuredContent = env.Data
	}
	return result
}
