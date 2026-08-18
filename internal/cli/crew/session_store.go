package crew

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	backendAnthropicAPI = "anthropic_api"
	sessionsFileName    = "sessions.json"
	sessionsDirName     = "sessions"
)

// sessionHashRe matches the on-disk name of an anthropic_api transcript:
// the first 16 hex chars of sha256(key).
var sessionHashRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// storedSession mirrors one row of sessions.json as written by the addon.
// The legacy shape (a bare session id string) is still accepted on read, in
// step with the addon's own migration.
type storedSession struct {
	SessionID string    `json:"session_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

func loadSessionAgent(deps sessionDeps, name string) (string, sessionAgentDoc, int) {
	var doc sessionAgentDoc
	if !hireNameRe.MatchString(name) {
		fmt.Fprintf(deps.Stderr, "shipyard crew session: invalid name %q\n", name)
		return "", doc, sessionExitNotFound
	}
	agentDir := filepath.Join(deps.Home, "crew", name)
	raw, err := os.ReadFile(filepath.Join(agentDir, "agent.yaml"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(deps.Stderr, "shipyard crew session: crew member not found: %s\n", name)
			return "", doc, sessionExitNotFound
		}
		fmt.Fprintf(deps.Stderr, "shipyard crew session: read agent.yaml: %s\n", err)
		return "", doc, sessionExitNotFound
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard crew session: parse agent.yaml: %s\n", err)
		return "", doc, sessionExitNotFound
	}
	return agentDir, doc, sessionExitOK
}

// parseSessionTTL returns the configured TTL. An unparseable value yields a
// zero TTL plus a warning: listing state is more useful than refusing to
// print it because one field is malformed (the addon rejects it at load
// time anyway).
func parseSessionTTL(doc sessionAgentDoc) (time.Duration, error) {
	raw := strings.TrimSpace(doc.Conversation.TTL)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("conversation.ttl %q is not a valid duration: %w", raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("conversation.ttl %q is negative", raw)
	}
	return d, nil
}

// collectSessionRows reads whichever on-disk layout the agent's backend uses
// and returns rows sorted by key.
func collectSessionRows(agentDir string, doc sessionAgentDoc, ttl time.Duration, now time.Time) ([]sessionRow, error) {
	if doc.Backend.Type == backendAnthropicAPI {
		return collectAPISessionRows(agentDir, ttl, now)
	}
	return collectCLISessionRows(agentDir, ttl, now)
}

func collectCLISessionRows(agentDir string, ttl time.Duration, now time.Time) ([]sessionRow, error) {
	path := filepath.Join(agentDir, sessionsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []sessionRow{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", sessionsFileName, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []sessionRow{}, nil
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", sessionsFileName, err)
	}

	var legacyStamp time.Time
	rows := make([]sessionRow, 0, len(raw))
	for key, val := range raw {
		s, err := decodeStoredSession(val, path, &legacyStamp)
		if err != nil {
			return nil, fmt.Errorf("parse %s entry %q: %w", sessionsFileName, key, err)
		}
		rows = append(rows, newSessionRow(key, s.SessionID, s.UpdatedAt, ttl, now, false))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return rows, nil
}

// decodeStoredSession accepts both the current object shape and the legacy
// bare-string shape, which has no timestamp of its own and inherits the
// file's mtime — the same rule the addon applies when it migrates.
func decodeStoredSession(val json.RawMessage, path string, legacyStamp *time.Time) (storedSession, error) {
	trimmed := strings.TrimSpace(string(val))
	if strings.HasPrefix(trimmed, `"`) {
		var id string
		if err := json.Unmarshal([]byte(trimmed), &id); err != nil {
			return storedSession{}, err
		}
		if legacyStamp.IsZero() {
			if info, err := os.Stat(path); err == nil {
				*legacyStamp = info.ModTime()
			}
		}
		return storedSession{SessionID: id, UpdatedAt: *legacyStamp}, nil
	}
	var s storedSession
	if err := json.Unmarshal([]byte(trimmed), &s); err != nil {
		return storedSession{}, err
	}
	return s, nil
}

func collectAPISessionRows(agentDir string, ttl time.Duration, now time.Time) ([]sessionRow, error) {
	dir := filepath.Join(agentDir, sessionsDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []sessionRow{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", sessionsDirName, err)
	}
	rows := make([]sessionRow, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		hash := strings.TrimSuffix(e.Name(), ".jsonl")
		var stamp time.Time
		if info, err := e.Info(); err == nil {
			stamp = info.ModTime()
		}
		rows = append(rows, newSessionRow(hash, "", stamp, ttl, now, true))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return rows, nil
}

func newSessionRow(key, sessionID string, stamp time.Time, ttl time.Duration, now time.Time, hashed bool) sessionRow {
	r := sessionRow{Key: key, SessionID: sessionID, Hashed: hashed}
	if stamp.IsZero() {
		return r
	}
	updated := stamp
	age := int64(now.Sub(stamp).Seconds())
	r.UpdatedAt = &updated
	r.AgeSec = &age
	if ttl > 0 {
		left := int64((ttl - now.Sub(stamp)).Seconds())
		r.ExpiresIn = &left
		r.Expired = left < 0
	}
	return r
}

// clearSessionKey removes the stored session for a key. It reports whether
// anything was actually removed so the caller can distinguish "cleared" from
// "there was nothing there".
func clearSessionKey(agentDir string, doc sessionAgentDoc, key, hash string) (bool, error) {
	if doc.Backend.Type == backendAnthropicAPI {
		path := filepath.Join(agentDir, sessionsDirName, hash+".jsonl")
		if err := os.Remove(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			return false, fmt.Errorf("remove %s: %w", path, err)
		}
		return true, nil
	}

	path := filepath.Join(agentDir, sessionsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", sessionsFileName, err)
	}
	raw := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return false, fmt.Errorf("parse %s: %w", sessionsFileName, err)
		}
	}
	if _, ok := raw[key]; !ok {
		return false, nil
	}
	delete(raw, key)
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal %s: %w", sessionsFileName, err)
	}
	if err := writeFileAtomic(path, out, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// writeFileAtomic mirrors the addon's write discipline: a temp file in the
// same directory, then rename, so a concurrent reader never sees a partial
// sessions.json.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// lockSessionKey takes the same per-key flock the addon holds around a run,
// so clearing cannot land between a run's load and its save. The lock file
// and its hash-based name are a cross-module contract: the CLI must not
// import addon internals, so the layout is mirrored here (see
// addons/crew/internal/crew/conversation/lock.go).
func lockSessionKey(path string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir locks dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lock busy after %s", wait)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func hashSessionKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

func isSessionHash(s string) bool { return sessionHashRe.MatchString(s) }

func sessionKeyLabel(r sessionRow) string {
	if r.Hashed {
		return r.Key + " (hashed)"
	}
	return r.Key
}

func truncateID(id string) string {
	const max = 12
	if len(id) <= max {
		return id
	}
	return id[:max] + "…"
}

func formatLastUse(r sessionRow) string {
	if r.AgeSec == nil {
		return "-"
	}
	return humanizeSeconds(*r.AgeSec) + " ago"
}

func formatExpiry(r sessionRow, ttl time.Duration) string {
	if ttl <= 0 {
		return "never"
	}
	if r.ExpiresIn == nil {
		return "-"
	}
	if *r.ExpiresIn < 0 {
		return "expired"
	}
	return humanizeSeconds(*r.ExpiresIn)
}

// humanizeSeconds renders a duration at one unit of precision below its
// magnitude: "3d4h", "2h13m", "45s".
func humanizeSeconds(sec int64) string {
	if sec < 0 {
		sec = -sec
	}
	d := time.Duration(sec) * time.Second
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

func confirmSessionClear(stdin io.Reader, stdout io.Writer, name string, n int) bool {
	fmt.Fprintf(stdout, "Clear all %d stored conversation(s) of %q? [y/N]: ", n, name)
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func encodeOrFail(deps sessionDeps, err error) int {
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard crew session: encode json: %s\n", err)
		return sessionExitNotFound
	}
	return sessionExitOK
}
