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
type Stateful struct {
	fs FileSystem
}

// NewStateful returns a Stateful store. Passing nil falls back to
// OSFileSystem.
func NewStateful(fsys FileSystem) *Stateful {
	if fsys == nil {
		fsys = OSFileSystem{}
	}
	return &Stateful{fs: fsys}
}

var _ Store = (*Stateful)(nil)

const (
	sessionsFile                = "sessions.json"
	sessionsDir                 = "sessions"
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
	rendered, err := template.Render(tmpl, template.Context{
		Input: input,
		Env:   envMap(),
		Agent: map[string]string{
			"name": agent.Name,
			"dir":  agent.Dir,
		},
	})
	if err != nil {
		return "", fmt.Errorf("render conversation.key: %w", err)
	}
	if strings.TrimSpace(rendered) == "" {
		return "", errors.New("conversation.key rendered to empty string")
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

func (s *Stateful) cliPath(agent *crew.Agent) string {
	return filepath.Join(agent.Dir, sessionsFile)
}

func (s *Stateful) readCLIMap(agent *crew.Agent) (map[string]string, error) {
	data, err := s.fs.ReadFile(s.cliPath(agent))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read sessions.json: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]string{}, nil
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse sessions.json: %w", err)
	}
	return m, nil
}

func (s *Stateful) loadCLI(agent *crew.Agent, key string) (History, error) {
	m, err := s.readCLIMap(agent)
	if err != nil {
		return History{}, err
	}
	return History{SessionID: m[key]}, nil
}

func (s *Stateful) saveCLI(agent *crew.Agent, key string, h History) error {
	m, err := s.readCLIMap(agent)
	if err != nil {
		return err
	}
	if h.SessionID == "" {
		delete(m, key)
	} else {
		m[key] = h.SessionID
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sessions.json: %w", err)
	}
	if err := s.fs.MkdirAll(agent.Dir, sessionDirMode); err != nil {
		return fmt.Errorf("mkdir agent dir: %w", err)
	}
	if err := s.fs.WriteFile(s.cliPath(agent), data, sessionFileMode); err != nil {
		return fmt.Errorf("write sessions.json: %w", err)
	}
	return nil
}

// ---- API branch ----

func (s *Stateful) apiPath(agent *crew.Agent, key string) string {
	return filepath.Join(agent.Dir, sessionsDir, hashedKey(key)+".jsonl")
}

func (s *Stateful) loadAPI(agent *crew.Agent, key string) (History, error) {
	data, err := s.fs.ReadFile(s.apiPath(agent, key))
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
