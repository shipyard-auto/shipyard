package crew

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeLogs writes a JSONL log file at <home>/logs/crew/<date>.jsonl with the
// given lines. Each line is a raw JSON object written verbatim.
func writeLogs(t *testing.T, home, date string, lines []string) {
	t.Helper()
	dir := filepath.Join(home, "logs", "crew")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, date+".jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

func entry(ts string, agent, level, trace, msg string) string {
	return fmt.Sprintf(`{"ts":%q,"level":%q,"agent":%q,"trace_id":%q,"message":%q}`, ts, level, agent, trace, msg)
}

func TestRunLogs_EmptyLogsDir(t *testing.T) {
	home := t.TempDir()
	var stdout bytes.Buffer
	deps := logsDeps{Home: home, Stdout: &stdout}
	if err := runLogs(context.Background(), deps, "chat-bot", logsFlags{}); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected empty output, got %q", stdout.String())
	}
}

func TestRunLogs_FiltersByAgent(t *testing.T) {
	home := t.TempDir()
	writeLogs(t, home, "2026-04-21", []string{
		entry("2026-04-21T10:00:00Z", "chat-bot", "info", "t1", "hi"),
		entry("2026-04-21T10:01:00Z", "promo-hunter", "info", "t2", "not me"),
		entry("2026-04-21T10:02:00Z", "chat-bot", "error", "t3", "boom"),
	})

	var stdout bytes.Buffer
	deps := logsDeps{Home: home, Stdout: &stdout}
	if err := runLogs(context.Background(), deps, "chat-bot", logsFlags{}); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	out := stdout.String()
	if strings.Contains(out, "not me") {
		t.Errorf("expected promo-hunter to be filtered out, got %q", out)
	}
	if !strings.Contains(out, "hi") || !strings.Contains(out, "boom") {
		t.Errorf("expected both chat-bot entries, got %q", out)
	}
}

func TestRunLogs_JSONPassthrough(t *testing.T) {
	home := t.TempDir()
	line := entry("2026-04-21T10:00:00Z", "chat-bot", "info", "t1", "hi")
	writeLogs(t, home, "2026-04-21", []string{line})

	var stdout bytes.Buffer
	deps := logsDeps{Home: home, Stdout: &stdout}
	if err := runLogs(context.Background(), deps, "chat-bot", logsFlags{JSON: true}); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != line {
		t.Errorf("expected raw line %q, got %q", line, stdout.String())
	}
}

func TestRunLogs_TailLastN(t *testing.T) {
	home := t.TempDir()
	lines := []string{
		entry("2026-04-21T10:00:00Z", "chat-bot", "info", "t1", "one"),
		entry("2026-04-21T10:00:01Z", "chat-bot", "info", "t2", "two"),
		entry("2026-04-21T10:00:02Z", "chat-bot", "info", "t3", "three"),
		entry("2026-04-21T10:00:03Z", "chat-bot", "info", "t4", "four"),
		entry("2026-04-21T10:00:04Z", "chat-bot", "info", "t5", "five"),
	}
	writeLogs(t, home, "2026-04-21", lines)

	var stdout bytes.Buffer
	deps := logsDeps{Home: home, Stdout: &stdout}
	if err := runLogs(context.Background(), deps, "chat-bot", logsFlags{Tail: 3}); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	out := stdout.String()
	if strings.Contains(out, "one") || strings.Contains(out, "two") {
		t.Errorf("expected only last 3 entries, got %q", out)
	}
	for _, want := range []string{"three", "four", "five"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output %q", want, out)
		}
	}
}

func TestRunLogs_SinceFilter(t *testing.T) {
	home := t.TempDir()
	writeLogs(t, home, "2026-04-21", []string{
		entry("2026-04-21T09:00:00Z", "chat-bot", "info", "t1", "old"),
		entry("2026-04-21T10:45:00Z", "chat-bot", "info", "t2", "fresh"),
	})

	now, _ := time.Parse(time.RFC3339, "2026-04-21T11:00:00Z")
	var stdout bytes.Buffer
	deps := logsDeps{Home: home, Stdout: &stdout, Now: func() time.Time { return now }}

	if err := runLogs(context.Background(), deps, "chat-bot", logsFlags{Since: 30 * time.Minute}); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	out := stdout.String()
	if strings.Contains(out, "old") {
		t.Errorf("expected old entry to be dropped by --since, got %q", out)
	}
	if !strings.Contains(out, "fresh") {
		t.Errorf("expected fresh entry, got %q", out)
	}
}

func TestRunLogs_MultipleDaysChronological(t *testing.T) {
	home := t.TempDir()
	writeLogs(t, home, "2026-04-20", []string{
		entry("2026-04-20T10:00:00Z", "chat-bot", "info", "t1", "day1"),
	})
	writeLogs(t, home, "2026-04-21", []string{
		entry("2026-04-21T10:00:00Z", "chat-bot", "info", "t2", "day2"),
	})

	var stdout bytes.Buffer
	deps := logsDeps{Home: home, Stdout: &stdout}
	if err := runLogs(context.Background(), deps, "chat-bot", logsFlags{}); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	out := stdout.String()
	idx1 := strings.Index(out, "day1")
	idx2 := strings.Index(out, "day2")
	if idx1 < 0 || idx2 < 0 {
		t.Fatalf("missing entries: %q", out)
	}
	if idx1 >= idx2 {
		t.Errorf("expected day1 before day2, got %q", out)
	}
}

func TestRunLogs_SkipsMalformedLines(t *testing.T) {
	home := t.TempDir()
	writeLogs(t, home, "2026-04-21", []string{
		":::not json",
		entry("2026-04-21T10:00:00Z", "chat-bot", "info", "t1", "ok"),
		"",
	})

	var stdout bytes.Buffer
	deps := logsDeps{Home: home, Stdout: &stdout}
	if err := runLogs(context.Background(), deps, "chat-bot", logsFlags{}); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Errorf("expected ok entry, got %q", stdout.String())
	}
}

func TestRunLogs_FieldsAgentFallback(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "logs", "crew")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := `{"ts":"2026-04-21T10:00:00Z","level":"info","trace_id":"t1","message":"via fields","fields":{"agent":"chat-bot"}}`
	if err := os.WriteFile(filepath.Join(dir, "2026-04-21.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stdout bytes.Buffer
	deps := logsDeps{Home: home, Stdout: &stdout}
	if err := runLogs(context.Background(), deps, "chat-bot", logsFlags{}); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	if !strings.Contains(stdout.String(), "via fields") {
		t.Errorf("expected entry resolved via fields.agent, got %q", stdout.String())
	}
}

func TestRunLogs_FollowEmitsNewLines(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "logs", "crew", "2026-04-21.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initial := entry("2026-04-21T10:00:00Z", "chat-bot", "info", "t1", "initial")
	if err := os.WriteFile(path, []byte(initial+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pr.Close()

	deps := logsDeps{
		Home:           home,
		Stdout:         pw,
		FollowInterval: 20 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		done <- runLogs(ctx, deps, "chat-bot", logsFlags{Follow: true})
		pw.Close()
	}()

	// Read the initial line.
	buf := make([]byte, 1024)
	n, err := pr.Read(buf)
	if err != nil {
		t.Fatalf("read initial: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "initial") {
		t.Errorf("expected initial line, got %q", string(buf[:n]))
	}

	// Append a new line and expect it to show up.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	appended := entry("2026-04-21T10:00:05Z", "chat-bot", "info", "t2", "appended-line")
	if _, err := f.WriteString(appended + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	// Read the appended line with a generous timeout.
	deadline := time.Now().Add(2 * time.Second)
	accum := ""
	for time.Now().Before(deadline) {
		pr.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		m, _ := pr.Read(buf)
		accum += string(buf[:m])
		if strings.Contains(accum, "appended-line") {
			break
		}
	}
	if !strings.Contains(accum, "appended-line") {
		t.Errorf("expected appended-line, got %q", accum)
	}

	cancel()
	<-done
}

func TestRunLogs_TailZeroMeansAll(t *testing.T) {
	home := t.TempDir()
	lines := []string{
		entry("2026-04-21T10:00:00Z", "chat-bot", "info", "t1", "one"),
		entry("2026-04-21T10:00:01Z", "chat-bot", "info", "t2", "two"),
	}
	writeLogs(t, home, "2026-04-21", lines)

	var stdout bytes.Buffer
	deps := logsDeps{Home: home, Stdout: &stdout}
	if err := runLogs(context.Background(), deps, "chat-bot", logsFlags{Tail: 0}); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{"one", "two"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestNewLogsCmd_FlagsWired(t *testing.T) {
	cmd := newLogsCmd()
	for _, name := range []string{"follow", "since", "tail", "json"} {
		if cmd.Flag(name) == nil {
			t.Errorf("missing flag %q", name)
		}
	}
}
