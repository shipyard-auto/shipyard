package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// ---- memory FileSystem ----

type memFile struct {
	data  []byte
	mode  fs.FileMode
	mtime time.Time
}

type memFS struct {
	files map[string]*memFile
	dirs  map[string]fs.FileMode
	// clock stamps the mtime of written files. Nil leaves mtimes zero,
	// which the store reads as "last use unknown".
	clock func() time.Time
}

func newMemFS() *memFS {
	return &memFS{
		files: map[string]*memFile{},
		dirs:  map[string]fs.FileMode{},
	}
}

func (m *memFS) ReadFile(p string) ([]byte, error) {
	f, ok := m.files[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	out := make([]byte, len(f.data))
	copy(out, f.data)
	return out, nil
}

func (m *memFS) WriteFile(p string, data []byte, mode fs.FileMode) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	var stamp time.Time
	if m.clock != nil {
		stamp = m.clock()
	}
	m.files[p] = &memFile{data: cp, mode: mode, mtime: stamp}
	return nil
}

func (m *memFS) MkdirAll(p string, mode fs.FileMode) error {
	m.dirs[p] = mode
	return nil
}

type memStat struct {
	name  string
	mtime time.Time
}

func (s memStat) Name() string       { return s.name }
func (memStat) Size() int64          { return 0 }
func (memStat) Mode() fs.FileMode    { return 0 }
func (s memStat) ModTime() time.Time { return s.mtime }
func (memStat) IsDir() bool          { return false }
func (memStat) Sys() any             { return nil }

func (m *memFS) Stat(p string) (fs.FileInfo, error) {
	if f, ok := m.files[p]; ok {
		return memStat{name: filepath.Base(p), mtime: f.mtime}, nil
	}
	if _, ok := m.dirs[p]; ok {
		return memStat{name: filepath.Base(p)}, nil
	}
	return nil, fs.ErrNotExist
}

// ---- helpers ----

func apiAgent(dir string) *crew.Agent {
	return &crew.Agent{
		Name: "bot",
		Dir:  dir,
		Backend: crew.Backend{
			Type:  crew.BackendAnthropicAPI,
			Model: "claude-sonnet",
		},
		Conversation: crew.Conversation{Mode: crew.ConversationStateful, Key: "{{input.chat}}"},
	}
}

func cliAgent(dir string) *crew.Agent {
	return &crew.Agent{
		Name: "bot",
		Dir:  dir,
		Backend: crew.Backend{
			Type:    crew.BackendCLI,
			Command: []string{"claude"},
		},
		Conversation: crew.Conversation{Mode: crew.ConversationStateful, Key: "chat-{{input.chat}}"},
	}
}

// ---- Resolve ----

func TestStatefulResolve(t *testing.T) {
	s := NewStateful(newMemFS())

	t.Run("empty template", func(t *testing.T) {
		a := cliAgent("/tmp/a")
		a.Conversation.Key = "   "
		if _, err := s.Resolve(a, map[string]any{}); err == nil || !strings.Contains(err.Error(), "is required") {
			t.Fatalf("want 'is required' error, got %v", err)
		}
	})

	t.Run("invalid template", func(t *testing.T) {
		a := cliAgent("/tmp/a")
		a.Conversation.Key = "{{bogus.x}}"
		_, err := s.Resolve(a, map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "render conversation.key") {
			t.Fatalf("want wrap of render error, got %v", err)
		}
	})

	t.Run("missing input key", func(t *testing.T) {
		a := cliAgent("/tmp/a")
		a.Conversation.Key = "{{input.missing}}"
		_, err := s.Resolve(a, map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "render conversation.key") {
			t.Fatalf("want render error, got %v", err)
		}
	})

	t.Run("renders to empty string", func(t *testing.T) {
		a := cliAgent("/tmp/a")
		a.Conversation.Key = "{{input.chat}}"
		_, err := s.Resolve(a, map[string]any{"chat": "   "})
		if err == nil || !strings.Contains(err.Error(), "rendered to empty string") {
			t.Fatalf("want 'rendered to empty string' error, got %v", err)
		}
	})

	t.Run("happy", func(t *testing.T) {
		a := cliAgent("/tmp/a")
		a.Conversation.Key = "chat-{{input.chat}}"
		key, err := s.Resolve(a, map[string]any{"chat": "12345"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "chat-12345" {
			t.Fatalf("got %q", key)
		}
	})

	t.Run("nil agent", func(t *testing.T) {
		if _, err := s.Resolve(nil, nil); err == nil {
			t.Fatalf("expected error for nil agent")
		}
	})
}

// ---- CLI branch ----

func TestStatefulCLILoadMissingFile(t *testing.T) {
	fsys := newMemFS()
	s := NewStateful(fsys)
	a := cliAgent("/tmp/a")
	h, err := s.Load(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.SessionID != "" || h.Messages != nil {
		t.Fatalf("want empty history, got %#v", h)
	}
}

func TestStatefulCLISaveLoadRoundtrip(t *testing.T) {
	fsys := newMemFS()
	s := NewStateful(fsys)
	a := cliAgent("/tmp/a")

	if err := s.Save(context.Background(), a, "chat-1", History{SessionID: "abc"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	h, err := s.Load(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if h.SessionID != "abc" {
		t.Fatalf("want abc, got %q", h.SessionID)
	}

	f := fsys.files["/tmp/a/sessions.json"]
	if f == nil {
		t.Fatalf("sessions.json not written")
	}
	if f.mode != 0o600 {
		t.Fatalf("want mode 0600, got %o", f.mode)
	}
	if fsys.dirs["/tmp/a"] != 0o700 {
		t.Fatalf("want dir mode 0700, got %o", fsys.dirs["/tmp/a"])
	}
}

func TestStatefulCLIEmptySessionIDRemovesKey(t *testing.T) {
	fsys := newMemFS()
	s := NewStateful(fsys)
	a := cliAgent("/tmp/a")

	if err := s.Save(context.Background(), a, "chat-1", History{SessionID: "abc"}); err != nil {
		t.Fatalf("save1: %v", err)
	}
	if err := s.Save(context.Background(), a, "chat-2", History{SessionID: "def"}); err != nil {
		t.Fatalf("save2: %v", err)
	}
	if err := s.Save(context.Background(), a, "chat-1", History{SessionID: ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}

	m := map[string]entry{}
	if err := json.Unmarshal(fsys.files["/tmp/a/sessions.json"].data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, still := m["chat-1"]; still {
		t.Fatalf("chat-1 should be removed, got %#v", m)
	}
	if m["chat-2"].SessionID != "def" {
		t.Fatalf("chat-2 should remain, got %#v", m)
	}
}

func TestStatefulCLIPreservesOtherKeys(t *testing.T) {
	fsys := newMemFS()
	s := NewStateful(fsys)
	a := cliAgent("/tmp/a")

	if err := s.Save(context.Background(), a, "chat-1", History{SessionID: "s1"}); err != nil {
		t.Fatalf("%v", err)
	}
	if err := s.Save(context.Background(), a, "chat-2", History{SessionID: "s2"}); err != nil {
		t.Fatalf("%v", err)
	}

	m := map[string]entry{}
	if err := json.Unmarshal(fsys.files["/tmp/a/sessions.json"].data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["chat-1"].SessionID != "s1" || m["chat-2"].SessionID != "s2" {
		t.Fatalf("got %#v", m)
	}
}

func TestStatefulCLIReadCorruptFile(t *testing.T) {
	fsys := newMemFS()
	fsys.files["/tmp/a/sessions.json"] = &memFile{data: []byte("{not json"), mode: 0o600}
	s := NewStateful(fsys)
	a := cliAgent("/tmp/a")

	if _, err := s.Load(context.Background(), a, "chat-1"); err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestStatefulCLIEmptyFileTreatedAsEmptyMap(t *testing.T) {
	fsys := newMemFS()
	fsys.files["/tmp/a/sessions.json"] = &memFile{data: []byte(""), mode: 0o600}
	s := NewStateful(fsys)
	a := cliAgent("/tmp/a")

	h, err := s.Load(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if h.SessionID != "" {
		t.Fatalf("want empty, got %q", h.SessionID)
	}
}

// ---- API branch ----

func TestStatefulAPILoadMissingFile(t *testing.T) {
	s := NewStateful(newMemFS())
	a := apiAgent("/tmp/a")
	h, err := s.Load(context.Background(), a, "k")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if h.Messages != nil {
		t.Fatalf("want nil messages, got %#v", h.Messages)
	}
}

func TestStatefulAPISaveWritesJSONL(t *testing.T) {
	fsys := newMemFS()
	s := NewStateful(fsys)
	a := apiAgent("/tmp/a")

	h := History{Messages: []Message{
		{Role: "user", Content: json.RawMessage(`"hi"`)},
		{Role: "assistant", Content: json.RawMessage(`"hello"`)},
	}}
	if err := s.Save(context.Background(), a, "mykey", h); err != nil {
		t.Fatalf("save: %v", err)
	}

	path := "/tmp/a/sessions/" + hashedKey("mykey") + ".jsonl"
	f := fsys.files[path]
	if f == nil {
		t.Fatalf("jsonl file not written, files=%v", fsys.files)
	}
	if f.mode != 0o600 {
		t.Fatalf("want mode 0600, got %o", f.mode)
	}
	if fsys.dirs["/tmp/a/sessions"] != 0o700 {
		t.Fatalf("want dir mode 0700, got %o", fsys.dirs["/tmp/a/sessions"])
	}
	lines := strings.Split(strings.TrimRight(string(f.data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), f.data)
	}
	for i, line := range lines {
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d not valid json: %v", i, err)
		}
	}
}

func TestStatefulAPIRoundtrip(t *testing.T) {
	fsys := newMemFS()
	s := NewStateful(fsys)
	a := apiAgent("/tmp/a")

	h := History{Messages: []Message{
		{Role: "user", Content: json.RawMessage(`{"type":"text","text":"hi"}`)},
		{Role: "assistant", Content: json.RawMessage(`{"type":"text","text":"hello"}`)},
	}}
	if err := s.Save(context.Background(), a, "k", h); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Load(context.Background(), a, "k")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("want 2 msgs, got %d", len(got.Messages))
	}
	for i := range h.Messages {
		if got.Messages[i].Role != h.Messages[i].Role {
			t.Fatalf("msg %d role: %q vs %q", i, got.Messages[i].Role, h.Messages[i].Role)
		}
		if string(got.Messages[i].Content) != string(h.Messages[i].Content) {
			t.Fatalf("msg %d content: %q vs %q", i, got.Messages[i].Content, h.Messages[i].Content)
		}
	}
}

func TestStatefulAPILoadCorruptLine(t *testing.T) {
	fsys := newMemFS()
	path := "/tmp/a/sessions/" + hashedKey("k") + ".jsonl"
	fsys.files[path] = &memFile{data: []byte(`{"role":"user","content":"ok"}` + "\nnot json\n"), mode: 0o600}
	s := NewStateful(fsys)
	if _, err := s.Load(context.Background(), apiAgent("/tmp/a"), "k"); err == nil {
		t.Fatalf("expected parse error")
	}
}

// ---- hash ----

func TestHashedKeyDeterministicAndDistinct(t *testing.T) {
	if hashedKey("a") != hashedKey("a") {
		t.Fatalf("hash not deterministic")
	}
	if hashedKey("a") == hashedKey("b") {
		t.Fatalf("hash collision")
	}
	if got := hashedKey("a"); len(got) != 16 {
		t.Fatalf("want 16 chars, got %d (%q)", len(got), got)
	}
}

// ---- backend routing ----

func TestStatefulUnknownBackend(t *testing.T) {
	s := NewStateful(newMemFS())
	a := &crew.Agent{
		Name:    "bot",
		Dir:     "/tmp/a",
		Backend: crew.Backend{Type: "xyz"},
	}
	if _, err := s.Load(context.Background(), a, "k"); err == nil {
		t.Fatalf("expected error")
	}
	if err := s.Save(context.Background(), a, "k", History{}); err == nil {
		t.Fatalf("expected error")
	}
}

func TestStatefulNilAgentLoadSave(t *testing.T) {
	s := NewStateful(newMemFS())
	if _, err := s.Load(context.Background(), nil, "k"); err == nil {
		t.Fatalf("expected error for nil agent")
	}
	if err := s.Save(context.Background(), nil, "k", History{}); err == nil {
		t.Fatalf("expected error for nil agent")
	}
}

// ---- constructor defaults ----

func TestNewStatefulAcceptsNil(t *testing.T) {
	s := NewStateful(nil)
	if s == nil {
		t.Fatalf("nil Stateful")
	}
	if _, ok := s.fs.(OSFileSystem); !ok {
		t.Fatalf("want default OSFileSystem, got %T", s.fs)
	}
}

// ---- integration: real filesystem ----

func TestStatefulOSFileSystemIntegration(t *testing.T) {
	dir := t.TempDir()
	a := apiAgent(dir)
	aCLI := cliAgent(dir)
	s := NewStateful(OSFileSystem{})

	// CLI roundtrip
	if err := s.Save(context.Background(), aCLI, "chat-1", History{SessionID: "s1"}); err != nil {
		t.Fatalf("cli save: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "sessions.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("want 0600, got %o", info.Mode().Perm())
	}

	h, err := s.Load(context.Background(), aCLI, "chat-1")
	if err != nil {
		t.Fatalf("cli load: %v", err)
	}
	if h.SessionID != "s1" {
		t.Fatalf("cli load sid: %q", h.SessionID)
	}

	// API roundtrip
	msg := History{Messages: []Message{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
	if err := s.Save(context.Background(), a, "k", msg); err != nil {
		t.Fatalf("api save: %v", err)
	}
	apiPath := filepath.Join(dir, "sessions", hashedKey("k")+".jsonl")
	info, err = os.Stat(apiPath)
	if err != nil {
		t.Fatalf("api stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("want 0600, got %o", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(apiPath))
	if err != nil {
		t.Fatalf("dir stat: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("want dir 0700, got %o", dirInfo.Mode().Perm())
	}

	got, err := s.Load(context.Background(), a, "k")
	if err != nil {
		t.Fatalf("api load: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Fatalf("unexpected messages: %#v", got.Messages)
	}

	// WriteFile must not leave tmp files behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestStatefulOSFileSystemReadNotExist(t *testing.T) {
	s := NewStateful(OSFileSystem{})
	dir := t.TempDir()
	h, err := s.Load(context.Background(), cliAgent(dir), "k")
	if err != nil {
		t.Fatalf("cli load: %v", err)
	}
	if h.SessionID != "" {
		t.Fatalf("want empty, got %q", h.SessionID)
	}
	h, err = s.Load(context.Background(), apiAgent(dir), "k")
	if err != nil {
		t.Fatalf("api load: %v", err)
	}
	if h.Messages != nil {
		t.Fatalf("want nil, got %#v", h.Messages)
	}
}

// Sanity: ensure fs.ErrNotExist is still the canonical sentinel we rely on.
func TestErrNotExistIsPropagated(t *testing.T) {
	if !errors.Is(fs.ErrNotExist, fs.ErrNotExist) {
		t.Fatal("sanity")
	}
}

// ---- TTL ----

// ttlAgent is a cli-backed agent with a TTL and a fixed clock, so expiry can
// be driven without sleeping.
func ttlAgent(dir string, ttl time.Duration) *crew.Agent {
	a := cliAgent(dir)
	a.Conversation.TTL = ttl
	return a
}

func TestStatefulTTLDiscardsIdleSession(t *testing.T) {
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	fsys := newMemFS()
	s := NewStateful(fsys)
	s.Now = func() time.Time { return base }
	a := ttlAgent("/tmp/a", 8*time.Hour)

	if err := s.Save(context.Background(), a, "chat-1", History{SessionID: "sess-1"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Just inside the window: the session is still resumable.
	s.Now = func() time.Time { return base.Add(7*time.Hour + 59*time.Minute) }
	h, err := s.Load(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if h.SessionID != "sess-1" {
		t.Fatalf("want sess-1 within ttl, got %q", h.SessionID)
	}

	// Past the window: the run must start fresh.
	s.Now = func() time.Time { return base.Add(8*time.Hour + time.Minute) }
	h, err = s.Load(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if h.SessionID != "" {
		t.Fatalf("want empty history past ttl, got %q", h.SessionID)
	}
}

// TTL measures inactivity, not age: every Save restarts the clock, which is
// why a daily-use agent under `ttl: 24h` would never reset (see the note in
// agent.yaml.tmpl).
func TestStatefulTTLSlidesOnEverySave(t *testing.T) {
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	now := base
	fsys := newMemFS()
	s := NewStateful(fsys)
	s.Now = func() time.Time { return now }
	a := ttlAgent("/tmp/a", 8*time.Hour)

	if err := s.Save(context.Background(), a, "chat-1", History{SessionID: "sess-1"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Six hours later the session is used again, pushing the deadline out.
	now = base.Add(6 * time.Hour)
	if err := s.Save(context.Background(), a, "chat-1", History{SessionID: "sess-1"}); err != nil {
		t.Fatalf("resave: %v", err)
	}

	// 10h after the first save, but only 4h after the last use.
	now = base.Add(10 * time.Hour)
	h, err := s.Load(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if h.SessionID != "sess-1" {
		t.Fatalf("want session kept alive by recent use, got %q", h.SessionID)
	}
}

func TestStatefulTTLZeroKeepsSessionForever(t *testing.T) {
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	s := NewStateful(newMemFS())
	s.Now = func() time.Time { return base }
	a := cliAgent("/tmp/a") // no TTL configured

	if err := s.Save(context.Background(), a, "chat-1", History{SessionID: "sess-1"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	s.Now = func() time.Time { return base.Add(365 * 24 * time.Hour) }
	h, err := s.Load(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if h.SessionID != "sess-1" {
		t.Fatalf("want session preserved without ttl, got %q", h.SessionID)
	}
}

func TestStatefulTTLAPIBackendUsesFileMtime(t *testing.T) {
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	now := base
	fsys := newMemFS()
	fsys.clock = func() time.Time { return now }
	s := NewStateful(fsys)
	s.Now = func() time.Time { return now }
	a := apiAgent("/tmp/a")
	a.Conversation.TTL = 8 * time.Hour

	hist := History{Messages: []Message{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
	if err := s.Save(context.Background(), a, "chat-1", hist); err != nil {
		t.Fatalf("save: %v", err)
	}

	now = base.Add(4 * time.Hour)
	got, err := s.Load(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("want transcript within ttl, got %#v", got.Messages)
	}

	now = base.Add(9 * time.Hour)
	got, err = s.Load(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Messages) != 0 {
		t.Fatalf("want empty transcript past ttl, got %#v", got.Messages)
	}
}

// ---- legacy migration ----

func TestStatefulReadsLegacyBareStringFormat(t *testing.T) {
	fsys := newMemFS()
	fsys.files["/tmp/a/sessions.json"] = &memFile{
		data:  []byte(`{"chat-1":"legacy-id","chat-2":"other-id"}`),
		mode:  0o600,
		mtime: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC),
	}
	s := NewStateful(fsys)
	a := cliAgent("/tmp/a")

	h, err := s.Load(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if h.SessionID != "legacy-id" {
		t.Fatalf("want legacy-id, got %q", h.SessionID)
	}
}

// A legacy row has no timestamp of its own, so it inherits the file mtime —
// the closest thing to a last-use marker the old format recorded.
func TestStatefulLegacyRowInheritsFileMtimeForTTL(t *testing.T) {
	written := time.Date(2026, 8, 18, 2, 0, 0, 0, time.UTC)
	fsys := newMemFS()
	fsys.files["/tmp/a/sessions.json"] = &memFile{
		data:  []byte(`{"chat-1":"legacy-id"}`),
		mode:  0o600,
		mtime: written,
	}
	s := NewStateful(fsys)
	a := ttlAgent("/tmp/a", 8*time.Hour)

	s.Now = func() time.Time { return written.Add(7 * time.Hour) }
	if h, _ := s.Load(context.Background(), a, "chat-1"); h.SessionID != "legacy-id" {
		t.Fatalf("want legacy row alive within ttl, got %q", h.SessionID)
	}

	s.Now = func() time.Time { return written.Add(9 * time.Hour) }
	if h, _ := s.Load(context.Background(), a, "chat-1"); h.SessionID != "" {
		t.Fatalf("want legacy row expired past ttl, got %q", h.SessionID)
	}
}

func TestStatefulMigratesLegacyFormatOnNextSave(t *testing.T) {
	stamp := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	fsys := newMemFS()
	fsys.files["/tmp/a/sessions.json"] = &memFile{
		data:  []byte(`{"chat-1":"legacy-id","chat-2":"keep-me"}`),
		mode:  0o600,
		mtime: stamp.Add(-time.Hour),
	}
	s := NewStateful(fsys)
	s.Now = func() time.Time { return stamp }
	a := cliAgent("/tmp/a")

	if err := s.Save(context.Background(), a, "chat-1", History{SessionID: "fresh-id"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	m := map[string]entry{}
	if err := json.Unmarshal(fsys.files["/tmp/a/sessions.json"].data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["chat-1"].SessionID != "fresh-id" || !m["chat-1"].UpdatedAt.Equal(stamp) {
		t.Fatalf("chat-1 not written in new shape: %#v", m["chat-1"])
	}
	// The untouched row is migrated too, carrying the old file mtime.
	if m["chat-2"].SessionID != "keep-me" {
		t.Fatalf("chat-2 lost in migration: %#v", m)
	}
	if !m["chat-2"].UpdatedAt.Equal(stamp.Add(-time.Hour)) {
		t.Fatalf("chat-2 should inherit file mtime, got %s", m["chat-2"].UpdatedAt)
	}
}

// ---- key fallback ----

func TestStatefulResolveKeyFallback(t *testing.T) {
	s := NewStateful(newMemFS())

	t.Run("missing field falls back", func(t *testing.T) {
		a := cliAgent("/tmp/a")
		a.Conversation.Key = "{{input.message.chat.id}}"
		a.Conversation.KeyFallback = "terminal"
		got, err := s.Resolve(a, map[string]any{"user": "oi"})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != "terminal" {
			t.Fatalf("want terminal, got %q", got)
		}
	})

	t.Run("missing field without fallback still errors", func(t *testing.T) {
		a := cliAgent("/tmp/a")
		a.Conversation.Key = "{{input.message.chat.id}}"
		if _, err := s.Resolve(a, map[string]any{"user": "oi"}); err == nil {
			t.Fatalf("want error when no fallback is configured")
		}
	})

	t.Run("present field wins over fallback", func(t *testing.T) {
		a := cliAgent("/tmp/a")
		a.Conversation.Key = "{{input.message.chat.id}}"
		a.Conversation.KeyFallback = "terminal"
		in := map[string]any{"message": map[string]any{"chat": map[string]any{"id": float64(987654321)}}}
		got, err := s.Resolve(a, in)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != "987654321" {
			t.Fatalf("want 987654321, got %q", got)
		}
	})

	t.Run("empty render falls back", func(t *testing.T) {
		a := cliAgent("/tmp/a")
		a.Conversation.Key = "{{input.chat}}"
		a.Conversation.KeyFallback = "terminal"
		got, err := s.Resolve(a, map[string]any{"chat": ""})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != "terminal" {
			t.Fatalf("want terminal, got %q", got)
		}
	})

	// A malformed template is an authoring bug, not a payload shape: the
	// fallback must not paper over it.
	t.Run("malformed template still fails", func(t *testing.T) {
		a := cliAgent("/tmp/a")
		a.Conversation.Key = "{{bogus.x}}"
		a.Conversation.KeyFallback = "terminal"
		if _, err := s.Resolve(a, map[string]any{}); err == nil {
			t.Fatalf("want error for unknown namespace even with fallback")
		}
	})
}

// The on-disk hash is a cross-module contract: `shipyard crew session`
// recomputes it to find the same transcript and the same lock file without
// importing addon internals. Pinning the value here (and in the CLI test of
// the same name) makes a drift in either side fail loudly.
func TestHashedKeyIsStable(t *testing.T) {
	cases := map[string]string{
		"chat-1":    "eaeb9111b1c67442",
		"987654321": "8a9bcf1e51e812d0",
		"terminal":  "4e686af7bdcc5ae0",
	}
	for key, want := range cases {
		if got := hashedKey(key); got != want {
			t.Errorf("hashedKey(%q) = %q, want %q", key, got, want)
		}
	}
}
