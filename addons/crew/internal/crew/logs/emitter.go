package logs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Emitter interface {
	RunStart(e RunStartEvent)
	RunEnd(e RunEndEvent)
	ToolCall(e ToolCallEvent)
	Error(e ErrorEvent)
	Close() error
}

type fileEmitter struct {
	dir    string
	mu     sync.Mutex
	cur    *os.File
	curKey string
	now    func() time.Time
}

func NewFileEmitter(baseDir string) (Emitter, error) {
	if baseDir == "" {
		home := os.Getenv("SHIPYARD_HOME")
		if home == "" {
			u, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("resolve home: %w", err)
			}
			home = filepath.Join(u, ".shipyard")
		}
		baseDir = filepath.Join(home, "logs", "crew")
	}
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", baseDir, err)
	}
	return &fileEmitter{dir: baseDir, now: time.Now}, nil
}

func (e *fileEmitter) write(ev event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	key := ev.TS.UTC().Format("2006-01-02")
	if key != e.curKey || e.cur == nil {
		if e.cur != nil {
			_ = e.cur.Close()
			e.cur = nil
		}
		path := filepath.Join(e.dir, key+".jsonl")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		e.cur = f
		e.curKey = key
	}
	enc := json.NewEncoder(e.cur)
	_ = enc.Encode(ev)
}

func (e *fileEmitter) RunStart(ev RunStartEvent) {
	fields := map[string]any{}
	if ev.Input != nil {
		fields["input"] = ev.Input
	}
	if len(fields) == 0 {
		fields = nil
	}
	e.write(event{
		TS:      e.now(),
		Level:   "info",
		Type:    "run_start",
		TraceID: ev.TraceID,
		Agent:   ev.Agent,
		Source:  ev.Source,
		Fields:  fields,
	})
}

func (e *fileEmitter) RunEnd(ev RunEndEvent) {
	level := "info"
	if ev.Status == "error" {
		level = "error"
	}
	fields := map[string]any{
		"duration_ms":   ev.DurationMS,
		"input_tokens":  ev.InputTokens,
		"output_tokens": ev.OutputTokens,
		"status":        ev.Status,
	}
	if ev.ErrorMessage != "" {
		fields["error_message"] = ev.ErrorMessage
	}
	e.write(event{
		TS:      e.now(),
		Level:   level,
		Type:    "run_end",
		TraceID: ev.TraceID,
		Agent:   ev.Agent,
		Fields:  fields,
	})
}

func (e *fileEmitter) ToolCall(ev ToolCallEvent) {
	level := "info"
	if !ev.Ok {
		level = "error"
	}
	fields := map[string]any{
		"tool_name":   ev.ToolName,
		"protocol":    ev.Protocol,
		"duration_ms": ev.DurationMS,
		"ok":          ev.Ok,
	}
	if ev.Error != "" {
		fields["error"] = ev.Error
	}
	e.write(event{
		TS:      e.now(),
		Level:   level,
		Type:    "tool_call",
		TraceID: ev.TraceID,
		Agent:   ev.Agent,
		Fields:  fields,
	})
}

func (e *fileEmitter) Error(ev ErrorEvent) {
	fields := ev.Fields
	if fields == nil {
		fields = map[string]any{}
	} else {
		copied := make(map[string]any, len(fields)+1)
		for k, v := range fields {
			copied[k] = v
		}
		fields = copied
	}
	if ev.Message != "" {
		fields["message"] = ev.Message
	}
	if len(fields) == 0 {
		fields = nil
	}
	e.write(event{
		TS:      e.now(),
		Level:   "error",
		Type:    "error",
		TraceID: ev.TraceID,
		Agent:   ev.Agent,
		Fields:  fields,
	})
}

func (e *fileEmitter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cur == nil {
		return nil
	}
	err := e.cur.Close()
	e.cur = nil
	e.curKey = ""
	return err
}

type NopEmitter struct{}

func (NopEmitter) RunStart(RunStartEvent) {}
func (NopEmitter) RunEnd(RunEndEvent)     {}
func (NopEmitter) ToolCall(ToolCallEvent) {}
func (NopEmitter) Error(ErrorEvent)       {}
func (NopEmitter) Close() error           { return nil }

func NewNopEmitter() Emitter { return NopEmitter{} }
