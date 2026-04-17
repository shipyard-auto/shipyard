package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type memLogFS struct {
	mu    sync.Mutex
	files map[string]*memLogFileData
}

type memLogFileData struct {
	mu      sync.Mutex
	content []byte
	modTime time.Time
}

type memLogFile struct {
	data   *memLogFileData
	offset int64
}

type memFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
}

type memDirEntry struct {
	name string
	info memFileInfo
}

func newMemLogFS() *memLogFS {
	return &memLogFS{files: map[string]*memLogFileData{}}
}

func (m *memLogFS) writeFile(path string, content []byte, mod time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = &memLogFileData{content: append([]byte(nil), content...), modTime: mod}
}

func (m *memLogFS) appendFile(path string, content []byte, mod time.Time) {
	m.mu.Lock()
	data := m.files[path]
	if data == nil {
		data = &memLogFileData{}
		m.files[path] = data
	}
	m.mu.Unlock()
	data.mu.Lock()
	defer data.mu.Unlock()
	data.content = append(data.content, content...)
	data.modTime = mod
}

func (m *memLogFS) Open(name string) (logFile, error) {
	m.mu.Lock()
	data := m.files[name]
	m.mu.Unlock()
	if data == nil {
		return nil, fs.ErrNotExist
	}
	return &memLogFile{data: data}, nil
}

func (m *memLogFS) Stat(name string) (fs.FileInfo, error) {
	m.mu.Lock()
	data := m.files[name]
	m.mu.Unlock()
	if data == nil {
		return nil, fs.ErrNotExist
	}
	data.mu.Lock()
	defer data.mu.Unlock()
	return memFileInfo{name: filepath.Base(name), size: int64(len(data.content)), mode: 0600, modTime: data.modTime}, nil
}

func (m *memLogFS) ReadDir(name string) ([]fs.DirEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []fs.DirEntry
	prefix := name + string(filepath.Separator)
	for path, data := range m.files {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		data.mu.Lock()
		info := memFileInfo{name: filepath.Base(path), size: int64(len(data.content)), mode: 0600, modTime: data.modTime}
		data.mu.Unlock()
		out = append(out, memDirEntry{name: filepath.Base(path), info: info})
	}
	return out, nil
}

func (f *memLogFile) Read(p []byte) (int, error) {
	f.data.mu.Lock()
	defer f.data.mu.Unlock()
	if f.offset >= int64(len(f.data.content)) {
		return 0, io.EOF
	}
	n := copy(p, f.data.content[f.offset:])
	f.offset += int64(n)
	return n, nil
}

func (f *memLogFile) Seek(offset int64, whence int) (int64, error) {
	f.data.mu.Lock()
	defer f.data.mu.Unlock()
	switch whence {
	case io.SeekStart:
		f.offset = offset
	case io.SeekCurrent:
		f.offset += offset
	case io.SeekEnd:
		f.offset = int64(len(f.data.content)) + offset
	}
	if f.offset < 0 {
		f.offset = 0
	}
	return f.offset, nil
}

func (f *memLogFile) Close() error { return nil }

func (i memFileInfo) Name() string       { return i.name }
func (i memFileInfo) Size() int64        { return i.size }
func (i memFileInfo) Mode() fs.FileMode  { return i.mode }
func (i memFileInfo) ModTime() time.Time { return i.modTime }
func (i memFileInfo) IsDir() bool        { return false }
func (i memFileInfo) Sys() any           { return nil }

func (d memDirEntry) Name() string               { return d.name }
func (d memDirEntry) IsDir() bool                { return false }
func (d memDirEntry) Type() fs.FileMode          { return 0 }
func (d memDirEntry) Info() (fs.FileInfo, error) { return d.info, nil }

func logLine(ts, level, method, path string, status int, dur int64, auth, action, target string) string {
	return fmt.Sprintf(`{"timestamp":"%s","source":"fairway","level":"%s","event":"http_request","message":"fairway HTTP request handled","data":{"method":"%s","path":"%s","status":%d,"durationMs":%d,"remoteAddr":"127.0.0.1:1","action":"%s","target":"%s","exitCode":0,"authType":"%s","authResult":"ok","truncated":false}}`+"\n",
		ts, level, method, path, status, dur, action, target, auth)
}

func TestLogs_readsCurrentDayFile(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	fs := newMemLogFS()
	dir := "/logs"
	path := filepath.Join(dir, "2026-04-16.jsonl")
	fs.writeFile(path, []byte(logLine(now.Format(time.RFC3339), "info", "POST", "/hooks/github", 200, 123, "bearer", "cron.run", "ABC123")), now)
	deps := fairwayLogsDeps{reader: &fairwayLogReader{dir: dir, now: func() time.Time { return now }, fs: fs, sleep: func(time.Duration) {}}, now: func() time.Time { return now }}
	cmd := newFairwayLogsCmdWith(deps)
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "/hooks/github") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestLogs_dateFlag_readsSpecificFile(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	fs := newMemLogFS()
	dir := "/logs"
	path := filepath.Join(dir, "2026-01-15.jsonl")
	fs.writeFile(path, []byte(logLine(now.Format(time.RFC3339), "info", "POST", "/hooks/date", 200, 12, "bearer", "cron.run", "DATE")), now)
	cmd := newFairwayLogsCmdWith(fairwayLogsDeps{reader: &fairwayLogReader{dir: dir, now: func() time.Time { return now }, fs: fs, sleep: func(time.Duration) {}}})
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	mustSet(t, cmd, "date", "2026-01-15")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "/hooks/date") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestLogs_absentFile_errorClear(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	cmd := newFairwayLogsCmdWith(fairwayLogsDeps{reader: &fairwayLogReader{dir: "/logs", now: func() time.Time { return now }, fs: newMemLogFS(), sleep: func(time.Duration) {}}})
	mustSet(t, cmd, "date", "2026-01-15")
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %v", err)
	}
}

func TestLogs_sinceFilter_dropsOldEntries(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	fs := newMemLogFS()
	dir := "/logs"
	path := filepath.Join(dir, "2026-04-16.jsonl")
	content := logLine(now.Add(-20*time.Minute).Format(time.RFC3339), "info", "POST", "/old", 200, 10, "bearer", "cron.run", "OLD") +
		logLine(now.Add(-5*time.Minute).Format(time.RFC3339), "info", "POST", "/new", 200, 10, "bearer", "cron.run", "NEW")
	fs.writeFile(path, []byte(content), now)
	cmd := newFairwayLogsCmdWith(fairwayLogsDeps{reader: &fairwayLogReader{dir: dir, now: func() time.Time { return now }, fs: fs, sleep: func(time.Duration) {}}})
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	mustSet(t, cmd, "since", "10m")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(out.String(), "/old") || !strings.Contains(out.String(), "/new") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestLogs_levelFilter_dropsNonMatching(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	fs := newMemLogFS()
	dir := "/logs"
	path := filepath.Join(dir, "2026-04-16.jsonl")
	content := logLine(now.Format(time.RFC3339), "info", "POST", "/info", 200, 10, "bearer", "cron.run", "OK") +
		logLine(now.Format(time.RFC3339), "error", "POST", "/error", 500, 10, "bearer", "cron.run", "ERR")
	fs.writeFile(path, []byte(content), now)
	cmd := newFairwayLogsCmdWith(fairwayLogsDeps{reader: &fairwayLogReader{dir: dir, now: func() time.Time { return now }, fs: fs, sleep: func(time.Duration) {}}})
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	mustSet(t, cmd, "level", "error")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(out.String(), "/info") || !strings.Contains(out.String(), "/error") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestLogs_prettyRenderFormat(t *testing.T) {
	event, ok, err := parseFairwayLogLine([]byte(strings.TrimSpace(logLine("2026-04-16T12:34:56Z", "info", "POST", "/hooks/github", 200, 123, "bearer", "cron.run", "ABC123"))), time.Time{}, "")
	if err != nil || !ok {
		t.Fatalf("parseFairwayLogLine() err=%v ok=%v", err, ok)
	}
	out := &bytes.Buffer{}
	renderFairwayLogPretty(out, event)
	got := out.String()
	if !strings.Contains(got, "2026-04-16 12:34:56") || !strings.Contains(got, "POST /hooks/github") || !strings.Contains(got, "123ms") {
		t.Fatalf("output = %q", got)
	}
}

func TestLogs_jsonPassthrough(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	line := strings.TrimSpace(logLine(now.Format(time.RFC3339), "info", "POST", "/hooks/github", 200, 123, "bearer", "cron.run", "ABC123"))
	fs := newMemLogFS()
	dir := "/logs"
	path := filepath.Join(dir, "2026-04-16.jsonl")
	fs.writeFile(path, []byte(line+"\n"), now)
	cmd := newFairwayLogsCmdWith(fairwayLogsDeps{reader: &fairwayLogReader{dir: dir, now: func() time.Time { return now }, fs: fs, sleep: func(time.Duration) {}}})
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	mustSet(t, cmd, "json", "true")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.TrimSpace(out.String()) != line {
		t.Fatalf("output = %q", out.String())
	}
}

func TestLogs_follow_seesNewAppends(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	fs := newMemLogFS()
	dir := "/logs"
	path := filepath.Join(dir, "2026-04-16.jsonl")
	fs.writeFile(path, nil, now)
	reader := &fairwayLogReader{dir: dir, now: func() time.Time { return now }, fs: fs, sleep: func(time.Duration) {}}
	out := &bytes.Buffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- followFairwayLogs(ctx, reader, []string{path}, time.Time{}, "", false, true, out) }()
	for i := 0; i < 3; i++ {
		fs.appendFile(path, []byte(logLine(now.Add(time.Duration(i)*time.Second).Format(time.RFC3339), "info", "POST", fmt.Sprintf("/hooks/%d", i), 200, 10, "bearer", "cron.run", "ABC")), now)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(out.String(), "/hooks/") == 3 {
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("followFairwayLogs() error = %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	_ = <-done
	t.Fatalf("output = %q; want 3 appended lines", out.String())
}

func TestLogs_follow_rotationHandling(t *testing.T) {
	current := time.Date(2026, 4, 16, 23, 59, 59, 0, time.UTC)
	fs := newMemLogFS()
	dir := "/logs"
	oldPath := filepath.Join(dir, "2026-04-16.jsonl")
	newPath := filepath.Join(dir, "2026-04-17.jsonl")
	fs.writeFile(oldPath, []byte(logLine(current.Format(time.RFC3339), "info", "POST", "/old", 200, 10, "bearer", "cron.run", "OLD")), current)
	reader := &fairwayLogReader{dir: dir, now: func() time.Time { return current }, fs: fs, sleep: func(time.Duration) {}}
	out := &bytes.Buffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- followFairwayLogs(ctx, reader, []string{oldPath}, time.Time{}, "", false, true, out) }()
	time.Sleep(20 * time.Millisecond)
	current = current.Add(2 * time.Second)
	fs.writeFile(newPath, []byte(logLine(current.Format(time.RFC3339), "info", "POST", "/new", 200, 10, "bearer", "cron.run", "NEW")), current)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "/new") {
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("followFairwayLogs() error = %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	_ = <-done
	t.Fatalf("output = %q; want rotated file line", out.String())
}
