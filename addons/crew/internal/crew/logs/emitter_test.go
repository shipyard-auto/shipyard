package logs

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestEmitter(t *testing.T, dir string, now func() time.Time) *fileEmitter {
	t.Helper()
	em, err := NewFileEmitter(dir)
	if err != nil {
		t.Fatalf("NewFileEmitter: %v", err)
	}
	fe := em.(*fileEmitter)
	if now != nil {
		fe.now = now
	}
	return fe
}

func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("parse line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func TestFileEmitter_WritesJSONL(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	em := newTestEmitter(t, dir, func() time.Time { return fixed })

	em.RunStart(RunStartEvent{TraceID: "t1", Agent: "alpha", Source: "manual", Input: map[string]any{"k": "v"}})
	em.ToolCall(ToolCallEvent{TraceID: "t1", Agent: "alpha", ToolName: "fetch", Protocol: "http", DurationMS: 12, Ok: true})
	em.RunEnd(RunEndEvent{TraceID: "t1", Agent: "alpha", DurationMS: 42, InputTokens: 5, OutputTokens: 7, Status: "success"})
	em.Error(ErrorEvent{TraceID: "t1", Agent: "alpha", Message: "boom"})

	if err := em.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	path := filepath.Join(dir, "2026-04-20.jsonl")
	lines := readJSONL(t, path)
	if len(lines) != 4 {
		t.Fatalf("want 4 lines, got %d", len(lines))
	}

	wantTypes := []string{"run_start", "tool_call", "run_end", "error"}
	for i, m := range lines {
		for _, k := range []string{"ts", "level", "type", "agent"} {
			if _, ok := m[k]; !ok {
				t.Errorf("line %d missing field %q: %v", i, k, m)
			}
		}
		if m["type"] != wantTypes[i] {
			t.Errorf("line %d type: want %q got %v", i, wantTypes[i], m["type"])
		}
		if m["agent"] != "alpha" {
			t.Errorf("line %d agent: want alpha got %v", i, m["agent"])
		}
	}

	if lines[3]["level"] != "error" {
		t.Errorf("error event level: want error got %v", lines[3]["level"])
	}
}

func TestFileEmitter_RotationByDate(t *testing.T) {
	dir := t.TempDir()
	var current atomic.Value
	current.Store(time.Date(2026, 4, 20, 23, 59, 0, 0, time.UTC))
	em := newTestEmitter(t, dir, func() time.Time { return current.Load().(time.Time) })

	em.RunStart(RunStartEvent{TraceID: "a", Agent: "alpha"})

	current.Store(time.Date(2026, 4, 21, 0, 0, 1, 0, time.UTC))
	em.RunStart(RunStartEvent{TraceID: "b", Agent: "alpha"})

	if err := em.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for _, name := range []string{"2026-04-20.jsonl", "2026-04-21.jsonl"} {
		p := filepath.Join(dir, name)
		lines := readJSONL(t, p)
		if len(lines) != 1 {
			t.Errorf("%s: want 1 line, got %d", name, len(lines))
		}
	}
}

func TestFileEmitter_Concurrent(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	em := newTestEmitter(t, dir, func() time.Time { return fixed })

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			em.ToolCall(ToolCallEvent{Agent: "alpha", ToolName: "t", Protocol: "exec", Ok: true})
		}()
	}
	wg.Wait()

	if err := em.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := readJSONL(t, filepath.Join(dir, "2026-04-20.jsonl"))
	if len(lines) != N {
		t.Fatalf("want %d lines, got %d", N, len(lines))
	}
}

func TestFileEmitter_CreatesDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "nested", "logs")
	em, err := NewFileEmitter(base)
	if err != nil {
		t.Fatalf("NewFileEmitter: %v", err)
	}
	defer em.Close()

	info, err := os.Stat(base)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode: want 0700, got %o", mode)
	}

	em.RunStart(RunStartEvent{Agent: "alpha"})
	_ = em.Close()

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var jsonl os.DirEntry
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".jsonl" {
			jsonl = e
			break
		}
	}
	if jsonl == nil {
		t.Fatalf("no jsonl file created")
	}
	fi, err := os.Stat(filepath.Join(base, jsonl.Name()))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode: want 0600, got %o", mode)
	}
}

func TestFileEmitter_CloseReopens(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	em := newTestEmitter(t, dir, func() time.Time { return fixed })

	em.RunStart(RunStartEvent{Agent: "alpha", TraceID: "t1"})
	if err := em.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	em.RunStart(RunStartEvent{Agent: "alpha", TraceID: "t2"})
	if err := em.Close(); err != nil {
		t.Fatalf("close2: %v", err)
	}

	lines := readJSONL(t, filepath.Join(dir, "2026-04-20.jsonl"))
	if len(lines) != 2 {
		t.Fatalf("want 2 lines after reopen, got %d", len(lines))
	}
}

func TestFileEmitter_DefaultBaseDirFromShipyardHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPYARD_HOME", home)

	em, err := NewFileEmitter("")
	if err != nil {
		t.Fatalf("NewFileEmitter: %v", err)
	}
	defer em.Close()

	want := filepath.Join(home, "logs", "crew")
	if fe := em.(*fileEmitter); fe.dir != want {
		t.Errorf("dir: want %q got %q", want, fe.dir)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("default dir not created: %v", err)
	}
}

func TestFileEmitter_RunEndErrorStatus(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	em := newTestEmitter(t, dir, func() time.Time { return fixed })

	em.RunEnd(RunEndEvent{TraceID: "t", Agent: "alpha", Status: "error", ErrorMessage: "boom", DurationMS: 9})
	_ = em.Close()

	lines := readJSONL(t, filepath.Join(dir, "2026-04-20.jsonl"))
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	if lines[0]["level"] != "error" {
		t.Errorf("level: want error got %v", lines[0]["level"])
	}
	fields := lines[0]["fields"].(map[string]any)
	if fields["error_message"] != "boom" {
		t.Errorf("error_message: want boom got %v", fields["error_message"])
	}
}

func TestFileEmitter_ToolCallFailure(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	em := newTestEmitter(t, dir, func() time.Time { return fixed })

	em.ToolCall(ToolCallEvent{Agent: "alpha", ToolName: "x", Protocol: "exec", Ok: false, Error: "exit 2"})
	_ = em.Close()

	lines := readJSONL(t, filepath.Join(dir, "2026-04-20.jsonl"))
	if lines[0]["level"] != "error" {
		t.Errorf("level: want error got %v", lines[0]["level"])
	}
	fields := lines[0]["fields"].(map[string]any)
	if fields["error"] != "exit 2" {
		t.Errorf("error field: want 'exit 2' got %v", fields["error"])
	}
	if fields["ok"] != false {
		t.Errorf("ok: want false got %v", fields["ok"])
	}
}

func TestFileEmitter_ErrorWithExistingFields(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	em := newTestEmitter(t, dir, func() time.Time { return fixed })

	input := map[string]any{"extra": "ctx"}
	em.Error(ErrorEvent{Agent: "alpha", Message: "kaboom", Fields: input})
	_ = em.Close()

	if _, ok := input["message"]; ok {
		t.Errorf("input fields map must not be mutated by Emitter.Error")
	}

	lines := readJSONL(t, filepath.Join(dir, "2026-04-20.jsonl"))
	fields := lines[0]["fields"].(map[string]any)
	if fields["extra"] != "ctx" {
		t.Errorf("extra: want ctx got %v", fields["extra"])
	}
	if fields["message"] != "kaboom" {
		t.Errorf("message: want kaboom got %v", fields["message"])
	}
}

func TestFileEmitter_CloseWhenNotOpen(t *testing.T) {
	dir := t.TempDir()
	em := newTestEmitter(t, dir, nil)
	if err := em.Close(); err != nil {
		t.Fatalf("close without writes: %v", err)
	}
}

func TestNopEmitter_NoPanics(t *testing.T) {
	em := NewNopEmitter()
	em.RunStart(RunStartEvent{Agent: "a"})
	em.RunEnd(RunEndEvent{Agent: "a", Status: "success"})
	em.ToolCall(ToolCallEvent{Agent: "a", Ok: true})
	em.Error(ErrorEvent{Agent: "a", Message: "x"})
	if err := em.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
