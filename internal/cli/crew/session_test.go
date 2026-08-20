package crew

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const statefulAgentYAML = `schema_version: "1"
name: tg
backend:
  type: cli
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateful
  key: "{{input.message.chat.id}}"
  ttl: 8h
`

const statelessAgentYAML = `schema_version: "1"
name: plain
backend:
  type: cli
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateless
`

func writeSessions(t *testing.T, home, agent, body string) string {
	t.Helper()
	dir := filepath.Join(home, "crew", agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "sessions.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write sessions.json: %v", err)
	}
	return path
}

func sessionTestDeps(home string, now time.Time, stdout, stderr *bytes.Buffer) sessionDeps {
	return sessionDeps{
		Home:   home,
		Stdout: stdout,
		Stderr: stderr,
		Stdin:  strings.NewReader(""),
		Now:    func() time.Time { return now },
		IsTTY:  func() bool { return false },
	}
}

func TestSessionList_TableShowsAgeAndExpiry(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	writeAgent(t, home, "tg", statefulAgentYAML)
	writeSessions(t, home, "tg", `{
	  "987654321": {"session_id": "sess-aaaaaaaaaaaa", "updated_at": "2026-08-18T10:00:00Z"},
	  "terminal":  {"session_id": "sess-b", "updated_at": "2026-08-18T01:00:00Z"}
	}`)

	var stdout, stderr bytes.Buffer
	if code := runSessionList(sessionTestDeps(home, now, &stdout, &stderr), "tg", false); code != sessionExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "987654321") || !strings.Contains(out, "2h0m ago") {
		t.Fatalf("missing live row: %q", out)
	}
	// 11h idle against an 8h TTL.
	if !strings.Contains(out, "expired") {
		t.Fatalf("stale row should read as expired: %q", out)
	}
}

func TestSessionList_JSON(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	writeAgent(t, home, "tg", statefulAgentYAML)
	writeSessions(t, home, "tg", `{"987654321": {"session_id": "s1", "updated_at": "2026-08-18T11:00:00Z"}}`)

	var stdout, stderr bytes.Buffer
	if code := runSessionList(sessionTestDeps(home, now, &stdout, &stderr), "tg", true); code != sessionExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	var rows []sessionRow
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("json: %v (body=%q)", err, stdout.String())
	}
	if len(rows) != 1 || rows[0].Key != "987654321" || rows[0].SessionID != "s1" {
		t.Fatalf("rows=%#v", rows)
	}
	if rows[0].AgeSec == nil || *rows[0].AgeSec != 3600 {
		t.Fatalf("age=%v, want 3600", rows[0].AgeSec)
	}
	if rows[0].ExpiresIn == nil || *rows[0].ExpiresIn != 7*3600 {
		t.Fatalf("expires_in=%v, want 25200", rows[0].ExpiresIn)
	}
	if rows[0].Expired {
		t.Fatalf("row should not be expired: %#v", rows[0])
	}
}

// Legacy rows (bare session id strings) predate the timestamp, so they read
// their age from the file mtime rather than being dropped.
func TestSessionList_LegacyFormatUsesFileMtime(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	writeAgent(t, home, "tg", statefulAgentYAML)
	path := writeSessions(t, home, "tg", `{"terminal": "legacy-id"}`)
	mtime := now.Add(-3 * time.Hour)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := runSessionList(sessionTestDeps(home, now, &stdout, &stderr), "tg", true); code != sessionExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	var rows []sessionRow
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != "legacy-id" {
		t.Fatalf("rows=%#v", rows)
	}
	if rows[0].AgeSec == nil || *rows[0].AgeSec != 3*3600 {
		t.Fatalf("age=%v, want 10800 from file mtime", rows[0].AgeSec)
	}
}

func TestSessionList_NoSessions(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "tg", statefulAgentYAML)
	var stdout, stderr bytes.Buffer
	if code := runSessionList(sessionTestDeps(home, time.Now(), &stdout, &stderr), "tg", false); code != sessionExitOK {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stdout.String(), "no sessions stored") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestSessionList_StatelessAgentSaysSo(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "plain", statelessAgentYAML)
	var stdout, stderr bytes.Buffer
	if code := runSessionList(sessionTestDeps(home, time.Now(), &stdout, &stderr), "plain", false); code != sessionExitOK {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stdout.String(), "not stateful") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestSessionList_UnknownAgent(t *testing.T) {
	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runSessionList(sessionTestDeps(home, time.Now(), &stdout, &stderr), "ghost", false); code != sessionExitNotFound {
		t.Fatalf("exit=%d, want %d", code, sessionExitNotFound)
	}
	if !strings.Contains(stderr.String(), "crew member not found") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

// Without a TTL the table must not imply an expiry that will never come.
func TestSessionList_NoTTLReportsNever(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "tg", strings.Replace(statefulAgentYAML, "  ttl: 8h\n", "", 1))
	writeSessions(t, home, "tg", `{"k": {"session_id": "s1", "updated_at": "2026-08-18T11:00:00Z"}}`)

	var stdout, stderr bytes.Buffer
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if code := runSessionList(sessionTestDeps(home, now, &stdout, &stderr), "tg", false); code != sessionExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "never") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestSessionClear_SingleKey(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "tg", statefulAgentYAML)
	path := writeSessions(t, home, "tg", `{
	  "a": {"session_id": "s1", "updated_at": "2026-08-18T11:00:00Z"},
	  "b": {"session_id": "s2", "updated_at": "2026-08-18T11:00:00Z"}
	}`)

	var stdout, stderr bytes.Buffer
	code := runSessionClear(sessionTestDeps(home, time.Now(), &stdout, &stderr), "tg", "a", true, false)
	if code != sessionExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json: %v", err)
	}
	if _, still := m["a"]; still {
		t.Fatalf("key a should be gone: %s", raw)
	}
	if _, kept := m["b"]; !kept {
		t.Fatalf("key b should remain: %s", raw)
	}
}

func TestSessionClear_UnknownKeyIsNotAnError(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "tg", statefulAgentYAML)
	writeSessions(t, home, "tg", `{"a": {"session_id": "s1", "updated_at": "2026-08-18T11:00:00Z"}}`)

	var stdout, stderr bytes.Buffer
	code := runSessionClear(sessionTestDeps(home, time.Now(), &stdout, &stderr), "tg", "zzz", true, false)
	if code != sessionExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no stored session") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestSessionClear_AllRefusesNonInteractiveWithoutYes(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "tg", statefulAgentYAML)
	path := writeSessions(t, home, "tg", `{"a": {"session_id": "s1", "updated_at": "2026-08-18T11:00:00Z"}}`)

	var stdout, stderr bytes.Buffer
	code := runSessionClear(sessionTestDeps(home, time.Now(), &stdout, &stderr), "tg", "", false, false)
	if code == sessionExitOK {
		t.Fatalf("clearing everything without --yes should fail")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "s1") {
		t.Fatalf("sessions.json should be untouched: %s", raw)
	}
}

func TestSessionClear_AllWithYes(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "tg", statefulAgentYAML)
	path := writeSessions(t, home, "tg", `{
	  "a": {"session_id": "s1", "updated_at": "2026-08-18T11:00:00Z"},
	  "b": "legacy-id"
	}`)

	var stdout, stderr bytes.Buffer
	code := runSessionClear(sessionTestDeps(home, time.Now(), &stdout, &stderr), "tg", "", false, true)
	if code != sessionExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("want empty sessions.json, got %s", raw)
	}
	if !strings.Contains(stdout.String(), "cleared 2 sessions") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

// A key held by a running agent must be reported, not yanked mid-run.
func TestSessionClear_SkipsKeyHeldByRunningAgent(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "tg", statefulAgentYAML)
	path := writeSessions(t, home, "tg", `{"a": {"session_id": "s1", "updated_at": "2026-08-18T11:00:00Z"}}`)

	lockPath := filepath.Join(home, "crew", "tg", "locks", hashSessionKey("a")+".lock")
	release, err := lockSessionKey(lockPath, time.Second)
	if err != nil {
		t.Fatalf("prime lock: %v", err)
	}
	defer release()

	var stdout, stderr bytes.Buffer
	deps := sessionTestDeps(home, time.Now(), &stdout, &stderr)
	deps.LockWait = 100 * time.Millisecond
	code := runSessionClear(deps, "tg", "a", true, false)
	if code != sessionExitBusy {
		t.Fatalf("exit=%d, want %d", code, sessionExitBusy)
	}
	if !strings.Contains(stderr.String(), "held by a running agent") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "s1") {
		t.Fatalf("busy key must survive: %s", raw)
	}
}

func TestSessionClear_APIBackendRemovesTranscript(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "api", `schema_version: "1"
name: api
backend:
  type: anthropic_api
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateful
  key: "{{input.chat}}"
`)
	dir := filepath.Join(home, "crew", "api", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	transcript := filepath.Join(dir, hashSessionKey("chat-1")+".jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runSessionClear(sessionTestDeps(home, time.Now(), &stdout, &stderr), "api", "chat-1", true, false)
	if code != sessionExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(transcript); !os.IsNotExist(err) {
		t.Fatalf("transcript should be gone, stat err=%v", err)
	}
}

// api transcripts are named by a hash of the key, so list reports the hash
// and marks the row rather than inventing a plaintext key.
func TestSessionList_APIBackendReportsHashedKeys(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "api", `schema_version: "1"
name: api
backend:
  type: anthropic_api
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateful
  key: "{{input.chat}}"
`)
	dir := filepath.Join(home, "crew", "api", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	hash := hashSessionKey("chat-1")
	if err := os.WriteFile(filepath.Join(dir, hash+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := runSessionList(sessionTestDeps(home, time.Now(), &stdout, &stderr), "api", false); code != sessionExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), hash+" (hashed)") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestSessionList_MalformedTTLWarnsAndStillLists(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "tg", strings.Replace(statefulAgentYAML, "ttl: 8h", "ttl: soon", 1))
	writeSessions(t, home, "tg", `{"a": {"session_id": "s1", "updated_at": "2026-08-18T11:00:00Z"}}`)

	var stdout, stderr bytes.Buffer
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if code := runSessionList(sessionTestDeps(home, now, &stdout, &stderr), "tg", false); code != sessionExitOK {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stderr.String(), "not a valid duration") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "s1") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

// Counterpart of the addon's TestHashedKeyIsStable: both sides must derive
// the same on-disk name for a key, since that name addresses the transcript
// and the lock file the addon writes.
func TestHashedKeyIsStable(t *testing.T) {
	cases := map[string]string{
		"chat-1":    "eaeb9111b1c67442",
		"987654321": "8a9bcf1e51e812d0",
		"terminal":  "4e686af7bdcc5ae0",
	}
	for key, want := range cases {
		if got := hashSessionKey(key); got != want {
			t.Errorf("hashSessionKey(%q) = %q, want %q", key, got, want)
		}
	}
}
