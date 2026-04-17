package fairway_test

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// makeEvent returns a minimal RequestEvent for testing.
func makeEvent(path string) fairway.RequestEvent {
	return fairway.RequestEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Source:    "fairway",
		Level:     "info",
		Event:     "request",
		Message:   path,
		Data: fairway.EventData{
			Method:     "POST",
			Path:       path,
			Status:     200,
			DurationMs: 5,
			RemoteAddr: "127.0.0.1:9999",
		},
	}
}

func TestLog_writesValidJSONLine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger, err := fairway.NewRequestLogger(dir, nil)
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer logger.Close() //nolint:errcheck

	event := makeEvent("/test-path")
	if err := logger.Log(event); err != nil {
		t.Fatalf("Log: %v", err)
	}

	// Find the log file.
	day := time.Now().UTC().Format("2006-01-02")
	logFile := filepath.Join(dir, day+".jsonl")

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var got fairway.RequestEvent
	if err := json.Unmarshal(data[:len(data)-1], &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Data.Path != "/test-path" {
		t.Errorf("Data.Path = %q; want %q", got.Data.Path, "/test-path")
	}
}

func TestLog_jsonlParseableLineByLineWithDecoder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger, err := fairway.NewRequestLogger(dir, func() time.Time {
		return time.Date(2026, 4, 17, 8, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer logger.Close() //nolint:errcheck

	if err := logger.Log(makeEvent("/one")); err != nil {
		t.Fatalf("Log /one: %v", err)
	}
	if err := logger.Log(makeEvent("/two")); err != nil {
		t.Fatalf("Log /two: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(filepath.Join(dir, "2026-04-17.jsonl"))
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var got []fairway.RequestEvent
	for {
		var evt fairway.RequestEvent
		if err := dec.Decode(&evt); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("Decode: %v", err)
		}
		got = append(got, evt)
	}

	if len(got) != 2 {
		t.Fatalf("decoded %d events; want 2", len(got))
	}
	if got[0].Data.Path != "/one" || got[1].Data.Path != "/two" {
		t.Fatalf("decoded paths = %q, %q; want /one, /two", got[0].Data.Path, got[1].Data.Path)
	}
}

func TestLog_rotatesOnDayChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Fake clock: first call returns day1, second call returns day2.
	day1 := time.Date(2026, 4, 16, 23, 59, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	calls := 0
	fakeClock := func() time.Time {
		calls++
		if calls == 1 {
			return day1
		}
		return day2
	}

	logger, err := fairway.NewRequestLogger(dir, fakeClock)
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer logger.Close() //nolint:errcheck

	// First log → day1 file.
	if err := logger.Log(makeEvent("/day1")); err != nil {
		t.Fatalf("Log day1: %v", err)
	}

	// Second log → day2 file (day changed).
	if err := logger.Log(makeEvent("/day2")); err != nil {
		t.Fatalf("Log day2: %v", err)
	}

	file1 := filepath.Join(dir, "2026-04-16.jsonl")
	file2 := filepath.Join(dir, "2026-04-17.jsonl")

	if _, err := os.Stat(file1); err != nil {
		t.Errorf("day1 log file not created: %v", err)
	}
	if _, err := os.Stat(file2); err != nil {
		t.Errorf("day2 log file not created: %v", err)
	}

	// Verify content in each file.
	checkFirstLine(t, file1, "/day1")
	checkFirstLine(t, file2, "/day2")
}

// checkFirstLine reads the first JSONL line from path and checks the event path.
func checkFirstLine(t *testing.T, path, wantPath string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatalf("%s: no lines", path)
	}

	var evt fairway.RequestEvent
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Data.Path != wantPath {
		t.Errorf("%s: Data.Path = %q; want %q", path, evt.Data.Path, wantPath)
	}
}

func TestLog_sameDayKeepsFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fixed := time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)
	logger, err := fairway.NewRequestLogger(dir, func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer logger.Close() //nolint:errcheck

	for i := 0; i < 3; i++ {
		if err := logger.Log(makeEvent("/same")); err != nil {
			t.Fatalf("Log %d: %v", i, err)
		}
	}

	logFile := filepath.Join(dir, "2026-04-17.jsonl")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	lines := countLines(data)
	if lines != 3 {
		t.Errorf("lines = %d; want 3", lines)
	}
}

func countLines(data []byte) int {
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return n
}

func TestLog_concurrentWriters_noInterleaving(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fixed := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	logger, err := fairway.NewRequestLogger(dir, func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer logger.Close() //nolint:errcheck

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = logger.Log(makeEvent("/concurrent"))
		}()
	}
	wg.Wait()

	logFile := filepath.Join(dir, "2026-04-17.jsonl")
	f, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		var evt fairway.RequestEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Errorf("invalid JSON on line %d: %v", count+1, err)
		}
		count++
	}
	if count != n {
		t.Errorf("lines = %d; want %d", count, n)
	}
}

func TestLog_createsDirMode0700(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	dir := filepath.Join(base, "subdir")

	logger, err := fairway.NewRequestLogger(dir, nil)
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer logger.Close() //nolint:errcheck

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0700 {
		t.Errorf("dir perm = %04o; want 0700", perm)
	}
}

func TestNewRequestLogger_createDirFailure(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	parent := filepath.Join(base, "readonly")
	if err := os.MkdirAll(parent, 0500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0700) })

	_, err := fairway.NewRequestLogger(filepath.Join(parent, "logs"), nil)
	if err == nil {
		t.Fatal("expected NewRequestLogger to fail for read-only parent")
	}
}

func TestLog_fileMode0600(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fixed := time.Date(2026, 4, 17, 8, 0, 0, 0, time.UTC)
	logger, err := fairway.NewRequestLogger(dir, func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer logger.Close() //nolint:errcheck

	if err := logger.Log(makeEvent("/perm-test")); err != nil {
		t.Fatalf("Log: %v", err)
	}

	logFile := filepath.Join(dir, "2026-04-17.jsonl")
	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file perm = %04o; want 0600", perm)
	}
}

func TestClose_idempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger, err := fairway.NewRequestLogger(dir, nil)
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}

	// Log something so the file is opened.
	if err := logger.Log(makeEvent("/close-test")); err != nil {
		t.Fatalf("Log: %v", err)
	}

	// Close twice — second call must not panic or return an error.
	if err := logger.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestClose_flushesContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fixed := time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC)
	logger, err := fairway.NewRequestLogger(dir, func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}

	if err := logger.Log(makeEvent("/flush-test")); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	logFile := filepath.Join(dir, "2026-04-17.jsonl")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Error("log file is empty after Close")
	}
	// Verify it's valid JSON.
	var evt fairway.RequestEvent
	if err := json.Unmarshal(data[:len(data)-1], &evt); err != nil {
		t.Errorf("Unmarshal after Close: %v", err)
	}
}
