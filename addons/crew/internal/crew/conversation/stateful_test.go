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
	data []byte
	mode fs.FileMode
}

type memFS struct {
	files map[string]*memFile
	dirs  map[string]fs.FileMode
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
	m.files[p] = &memFile{data: cp, mode: mode}
	return nil
}

func (m *memFS) MkdirAll(p string, mode fs.FileMode) error {
	m.dirs[p] = mode
	return nil
}

type memStat struct{ name string }

func (s memStat) Name() string     { return s.name }
func (memStat) Size() int64        { return 0 }
func (memStat) Mode() fs.FileMode  { return 0 }
func (memStat) ModTime() time.Time { return time.Time{} }
func (memStat) IsDir() bool        { return false }
func (memStat) Sys() any           { return nil }

func (m *memFS) Stat(p string) (fs.FileInfo, error) {
	if _, ok := m.files[p]; ok {
		return memStat{name: filepath.Base(p)}, nil
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

	m := map[string]string{}
	if err := json.Unmarshal(fsys.files["/tmp/a/sessions.json"].data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, still := m["chat-1"]; still {
		t.Fatalf("chat-1 should be removed, got %#v", m)
	}
	if m["chat-2"] != "def" {
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

	m := map[string]string{}
	if err := json.Unmarshal(fsys.files["/tmp/a/sessions.json"].data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["chat-1"] != "s1" || m["chat-2"] != "s2" {
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
