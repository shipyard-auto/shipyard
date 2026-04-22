package logs

import "time"

type event struct {
	TS      time.Time      `json:"ts"`
	Level   string         `json:"level"`
	Type    string         `json:"type"`
	TraceID string         `json:"trace_id,omitempty"`
	Agent   string         `json:"agent"`
	Source  string         `json:"source,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

type RunStartEvent struct {
	TraceID string
	Agent   string
	Source  string
	Input   map[string]any
}

type RunEndEvent struct {
	TraceID      string
	Agent        string
	DurationMS   int64
	OutputTokens int
	InputTokens  int
	Status       string
	ErrorMessage string
}

type ToolCallEvent struct {
	TraceID    string
	Agent      string
	ToolName   string
	Protocol   string
	DurationMS int64
	Ok         bool
	Error      string
}

type ErrorEvent struct {
	TraceID string
	Agent   string
	Message string
	Fields  map[string]any
}
