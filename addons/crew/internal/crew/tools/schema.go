package tools

// BuildJSONSchema converts the compact "field -> type" map used in agent.yaml
// (see crew.Tool.InputSchema and crew.Tool.OutputSchema) into a minimal
// JSON-Schema object describing an object whose declared fields match the
// allowed crew types. It is deliberately lenient — no required list, no
// additionalProperties — so that callers (the Anthropic API tool_use loop
// and the internal MCP server) can extend the shape with their own
// conventions without fighting a strict schema.
//
// A nil or empty input returns an object schema with no properties, which is
// the correct default for a tool that takes no arguments.
func BuildJSONSchema(fields map[string]string) map[string]any {
	props := make(map[string]any, len(fields))
	for name, typ := range fields {
		props[name] = map[string]any{"type": mapSchemaType(typ)}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
	}
}

// mapSchemaType translates the crew domain type names ("string", "number",
// "boolean", "object", "array") into JSON-Schema primitive types. Unknown
// values fall back to "string" — the domain loader already rejects invalid
// types at agent.yaml parse time, so this is belt-and-suspenders.
func mapSchemaType(t string) string {
	switch t {
	case "number":
		return "number"
	case "boolean":
		return "boolean"
	case "object":
		return "object"
	case "array":
		return "array"
	default:
		return "string"
	}
}
