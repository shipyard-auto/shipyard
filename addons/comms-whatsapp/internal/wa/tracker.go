package wa

import (
	"sync"
	"time"
)

// Tracker holds the daemon's live status and persists it to status.json
// on every change and on Heartbeat. Safe for concurrent use: whatsmeow
// delivers events from its own goroutines.
type Tracker struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
	s    Status
	// writeErr keeps the last persistence failure so the daemon can log
	// it once instead of on every heartbeat.
	writeErr error
}

// NewTracker starts in StateConnecting for this process. An empty path
// keeps the status in memory only.
func NewTracker(path string, pid int, now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	t := now().UTC()
	return &Tracker{path: path, now: now, s: Status{PID: pid, State: StateConnecting, Since: t, HeartbeatAt: t}}
}

// SetState records a transition. Re-entering the same state keeps Since.
func (t *Tracker) SetState(st State, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now().UTC()
	if t.s.State != st {
		t.s.Since = now
	}
	t.s.State, t.s.Detail, t.s.HeartbeatAt = st, detail, now
	t.flushLocked()
}

// SetAccount records the logged-in identity.
func (t *Tracker) SetAccount(phone, pushName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.s.Account, t.s.PushName = phone, pushName
	t.flushLocked()
}

// MessageSeen bumps LastMessageAt in memory; the next heartbeat or state
// change persists it (one write per message would be wasteful).
func (t *Tracker) MessageSeen(at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if at.After(t.s.LastMessageAt) {
		t.s.LastMessageAt = at.UTC()
	}
}

// Heartbeat refreshes HeartbeatAt and persists. Returns the write error,
// if any.
func (t *Tracker) Heartbeat() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.s.HeartbeatAt = t.now().UTC()
	t.flushLocked()
	return t.writeErr
}

// Snapshot returns the current status.
func (t *Tracker) Snapshot() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s
}

func (t *Tracker) flushLocked() {
	if t.path == "" {
		return
	}
	t.writeErr = WriteStatus(t.path, t.s)
}
