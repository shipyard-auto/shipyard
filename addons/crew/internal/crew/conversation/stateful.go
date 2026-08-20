package conversation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/template"
)

// FileSystem abstracts the on-disk operations used by the stateful store so
// that tests can plug a fake. WriteFile must be atomic (tmp + rename) and
// honour the provided mode.
type FileSystem interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, mode fs.FileMode) error
	MkdirAll(path string, mode fs.FileMode) error
	Stat(path string) (fs.FileInfo, error)
}

// OSFileSystem is the default FileSystem backed by the os package. WriteFile
// writes to a temp file in the same directory and atomically renames it.
type OSFileSystem struct{}

func (OSFileSystem) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) }

func (OSFileSystem) MkdirAll(p string, m fs.FileMode) error { return os.MkdirAll(p, m) }

func (OSFileSystem) Stat(p string) (fs.FileInfo, error) { return os.Stat(p) }

func (OSFileSystem) WriteFile(p string, data []byte, m fs.FileMode) error {
	dir := filepath.Dir(p)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(m); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Stateful persists conversation history across runs. Behaviour depends on
// agent.Backend.Type: "cli" stores an opaque session id per key in a single
// sessions.json map; "anthropic_api" stores the full message history in a
// sessions/<hash16(key)>.jsonl file.
//
// When agent.Conversation.TTL is set, an entry untouched for longer than the
// TTL is ignored on Load and the run starts a fresh session. The window is
// sliding — it measures inactivity, not the age of the session.
type Stateful struct {
	fs FileSystem
	// Now is the clock used for TTL arithmetic. Nil means time.Now.
	Now func() time.Time
}

// NewStateful returns a Stateful store. Passing nil falls back to
// OSFileSystem.
func NewStateful(fsys FileSystem) *Stateful {
	if fsys == nil {
		fsys = OSFileSystem{}
	}
	return &Stateful{fs: fsys}
}

func (s *Stateful) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var _ Store = (*Stateful)(nil)

const (
	sessionsFile                = "sessions.json"
	sessionsDir                 = "sessions"
	locksDir                    = "locks"
	sessionFileMode fs.FileMode = 0o600
	sessionDirMode  fs.FileMode = 0o700
)

func (s *Stateful) Resolve(agent *crew.Agent, input map[string]any) (string, error) {
	if agent == nil {
		return "", errors.New("conversation: agent is required")
	}
	tmpl := agent.Conversation.Key
	if strings.TrimSpace(tmpl) == "" {
		return "", errors.New("conversation.key is required when mode=stateful")
	}
	fallback := strings.TrimSpace(agent.Conversation.KeyFallback)
	rendered, err := template.Render(tmpl, template.Context{
		Input: input,
		Env:   envMap(),
		Agent: map[string]string{
			"name": agent.Name,
			"dir":  agent.Dir,
		},
	})
	switch {
	case err == nil:
	case errors.Is(err, template.ErrMissingValue):
		// The payload simply lacks the fields the key references (a
		// terminal run of an agent keyed by chat id, say). That is a data
		// shape, not an authoring mistake, so it may degrade to the
		// configured fallback. Malformed templates fall through and stay
		// fatal.
		if fallback == "" {
			return "", fmt.Errorf("render conversation.key: %w", err)
		}
		return fallback, nil
	default:
		return "", fmt.Errorf("render conversation.key: %w", err)
	}
	if strings.TrimSpace(rendered) == "" {
		if fallback == "" {
			return "", errors.New("conversation.key rendered to empty string")
		}
		return fallback, nil
	}
	return rendered, nil
}

func (s *Stateful) Load(ctx context.Context, agent *crew.Agent, key string) (History, error) {
	if agent == nil {
		return History{}, errors.New("conversation: agent is required")
	}
	switch agent.Backend.Type {
	case crew.BackendCLI:
		return s.loadCLI(agent, key)
	case crew.BackendAnthropicAPI:
		return s.loadAPI(agent, key)
	default:
		return History{}, fmt.Errorf("unsupported backend type for stateful: %q", agent.Backend.Type)
	}
}

func (s *Stateful) Save(ctx context.Context, agent *crew.Agent, key string, h History) error {
	if agent == nil {
		return errors.New("conversation: agent is required")
	}
	switch agent.Backend.Type {
	case crew.BackendCLI:
		return s.saveCLI(agent, key, h)
	case crew.BackendAnthropicAPI:
		return s.saveAPI(agent, key, h)
	default:
		return fmt.Errorf("unsupported backend type for stateful: %q", agent.Backend.Type)
	}
}

// ---- CLI branch ----

// entry is one row of sessions.json: the opaque CLI session id plus the
// timestamp of the last run that used it, which is what TTL expiry measures.
// The core-side `shipyard crew session` command mirrors this shape rather
// than importing it — the CLI must not depend on addon internals.
type entry struct {
	SessionID string    `json:"session_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// readEntries returns the sessions.json map of an agent directory, migrating
// the legacy shape (key -> bare session id string) on the fly. Legacy rows
// have no timestamp of their own, so they inherit the file's mtime: the
// first TTL window after an upgrade is measured from the last write to
// sessions.json, which is the closest thing to a last-use marker the old
// format recorded. Migration is lazy — the upgraded shape reaches disk on
// the next Save.
func readEntries(fsys FileSystem, dir string) (map[string]entry, error) {
	if fsys == nil {
		fsys = OSFileSystem{}
	}
	path := filepath.Join(dir, sessionsFile)
	data, err := fsys.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return map[string]entry{}, nil
		}
		return nil, fmt.Errorf("read sessions.json: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]entry{}, nil
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse sessions.json: %w", err)
	}

	var legacyStamp time.Time
	out := make(map[string]entry, len(raw))
	for key, val := range raw {
		trimmed := bytes.TrimSpace(val)
		if len(trimmed) > 0 && trimmed[0] == '"' {
			// Legacy row: a bare session id string.
			var id string
			if err := json.Unmarshal(trimmed, &id); err != nil {
				return nil, fmt.Errorf("parse sessions.json entry %q: %w", key, err)
			}
			if legacyStamp.IsZero() {
				legacyStamp = fileModTime(fsys, path)
			}
			out[key] = entry{SessionID: id, UpdatedAt: legacyStamp}
			continue
		}
		var e entry
		if err := json.Unmarshal(trimmed, &e); err != nil {
			return nil, fmt.Errorf("parse sessions.json entry %q: %w", key, err)
		}
		out[key] = e
	}
	return out, nil
}

// writeEntries persists the sessions.json map of an agent directory.
func writeEntries(fsys FileSystem, dir string, entries map[string]entry) error {
	if fsys == nil {
		fsys = OSFileSystem{}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sessions.json: %w", err)
	}
	if err := fsys.MkdirAll(dir, sessionDirMode); err != nil {
		return fmt.Errorf("mkdir agent dir: %w", err)
	}
	if err := fsys.WriteFile(filepath.Join(dir, sessionsFile), data, sessionFileMode); err != nil {
		return fmt.Errorf("write sessions.json: %w", err)
	}
	return nil
}

func fileModTime(fsys FileSystem, path string) time.Time {
	info, err := fsys.Stat(path)
	if err != nil {
		// No mtime to inherit: treat the row as just-used so an upgrade
		// never drops a live session on its first read.
		return time.Now()
	}
	return info.ModTime()
}

func (s *Stateful) loadCLI(agent *crew.Agent, key string) (History, error) {
	m, err := readEntries(s.fs, agent.Dir)
	if err != nil {
		return History{}, err
	}
	e, ok := m[key]
	if !ok {
		return History{}, nil
	}
	if s.expired(agent, e.UpdatedAt) {
		return History{}, nil
	}
	return History{SessionID: e.SessionID}, nil
}

func (s *Stateful) saveCLI(agent *crew.Agent, key string, h History) error {
	m, err := readEntries(s.fs, agent.Dir)
	if err != nil {
		return err
	}
	if h.SessionID == "" {
		delete(m, key)
	} else {
		m[key] = entry{SessionID: h.SessionID, UpdatedAt: s.now()}
	}
	return writeEntries(s.fs, agent.Dir, m)
}

// expired reports whether an entry last used at stamp falls outside the
// agent's TTL window. A zero TTL disables expiry; a zero stamp is treated as
// live, since an unknown last-use is not evidence of staleness.
func (s *Stateful) expired(agent *crew.Agent, stamp time.Time) bool {
	ttl := agent.Conversation.TTL
	if ttl <= 0 || stamp.IsZero() {
		return false
	}
	return s.now().Sub(stamp) > ttl
}

// ---- API branch ----

func (s *Stateful) apiPath(agent *crew.Agent, key string) string {
	return filepath.Join(agent.Dir, sessionsDir, hashedKey(key)+".jsonl")
}

func (s *Stateful) loadAPI(agent *crew.Agent, key string) (History, error) {
	path := s.apiPath(agent, key)
	// The jsonl transcript carries no per-key metadata, so the file's own
	// mtime stands in for last use — every Save rewrites it.
	if agent.Conversation.TTL > 0 {
		if info, err := s.fs.Stat(path); err == nil && s.expired(agent, info.ModTime()) {
			return History{}, nil
		}
	}
	data, err := s.fs.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return History{}, nil
		}
		return History{}, fmt.Errorf("read session file: %w", err)
	}
	var msgs []Message
	for i, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			return History{}, fmt.Errorf("parse session line %d: %w", i+1, err)
		}
		msgs = append(msgs, m)
	}
	return History{Messages: msgs}, nil
}

func (s *Stateful) saveAPI(agent *crew.Agent, key string, h History) error {
	dir := filepath.Join(agent.Dir, sessionsDir)
	if err := s.fs.MkdirAll(dir, sessionDirMode); err != nil {
		return fmt.Errorf("mkdir sessions dir: %w", err)
	}
	var buf bytes.Buffer
	for i, msg := range h.Messages {
		line, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal message %d: %w", i, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := s.fs.WriteFile(s.apiPath(agent, key), buf.Bytes(), sessionFileMode); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	return nil
}

// ---- helpers ----

func hashedKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

func envMap() map[string]string {
	all := os.Environ()
	out := make(map[string]string, len(all))
	for _, kv := range all {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}
