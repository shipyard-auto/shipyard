package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Group membership changes (join, leave, promote, demote) of the groups
// whose members are tracked. Like messages, a row is written only while
// its group is tracked — the check is inside the INSERT — and rows expire
// after a TTL. Members are stored as keyed hashes computed by the caller:
// the archive never sees a member's phone number or JID.

// Member actions.
const (
	ActionJoin    = "join"
	ActionLeave   = "leave"
	ActionPromote = "promote"
	ActionDemote  = "demote"
)

// GroupEvent is one membership change of one member.
type GroupEvent struct {
	Seq       int64
	ID        string // stable: a redelivered change keeps it
	ChatJID   string
	ChatName  string // filled by reads
	Timestamp time.Time
	Action    string
	Member    string // hash
	Actor     string // hash; empty = unknown
	Via       string // invite_link|added|removed|left; empty = unknown
}

// TrackedGroup is a group whose membership changes are recorded.
type TrackedGroup struct {
	JID    string `json:"jid"`
	Name   string `json:"name"`
	Events int    `json:"events"` // stored, not yet expired
}

// SetMembersTracked turns membership tracking on or off for a group.
// Turning it off deletes the group's stored events, as unwatch does with
// messages; it returns how many were deleted.
func (s *Store) SetMembersTracked(ctx context.Context, jid string, on bool) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("archive: track members %s: %w", jid, err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE chats SET members_tracked = ? WHERE jid = ? AND kind = 'group'`, boolInt(on), jid)
	if err != nil {
		return 0, fmt.Errorf("archive: track members %s: %w", jid, err)
	}
	if err := requireRow(res, jid); err != nil {
		return 0, err
	}
	var n int64
	if !on {
		del, err := tx.ExecContext(ctx, `DELETE FROM group_events WHERE chat_jid = ?`, jid)
		if err != nil {
			return 0, fmt.Errorf("archive: untrack members %s: %w", jid, err)
		}
		n, _ = del.RowsAffected()
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("archive: track members %s: %w", jid, err)
	}
	return n, nil
}

// MembersTracked reports whether jid's membership changes are recorded.
func (s *Store) MembersTracked(ctx context.Context, jid string) (bool, error) {
	var on int
	err := s.db.QueryRowContext(ctx, `SELECT members_tracked FROM chats WHERE jid = ?`, jid).Scan(&on)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("archive: %w", err)
	}
	return on == 1, nil
}

// ListTrackedGroups returns the tracked groups ordered by name.
func (s *Store) ListTrackedGroups(ctx context.Context) ([]TrackedGroup, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.jid, c.name, (SELECT COUNT(*) FROM group_events e WHERE e.chat_jid = c.jid)
		FROM chats c WHERE c.members_tracked = 1 ORDER BY lower(c.name), c.jid`)
	if err != nil {
		return nil, fmt.Errorf("archive: tracked groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []TrackedGroup{}
	for rows.Next() {
		var g TrackedGroup
		if err := rows.Scan(&g.JID, &g.Name, &g.Events); err != nil {
			return nil, fmt.Errorf("archive: tracked groups: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// InsertGroupEvent stores e if its group is tracked and its id is new.
// Returns whether a row was written.
func (s *Store) InsertGroupEvent(ctx context.Context, e GroupEvent) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO group_events (id, chat_jid, ts, action, member, actor, via)
		SELECT ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM chats WHERE jid = ? AND members_tracked = 1)
		ON CONFLICT (id) DO NOTHING`,
		e.ID, e.ChatJID, e.Timestamp.Unix(), e.Action, e.Member, nullable(e.Actor), nullable(e.Via), e.ChatJID)
	if err != nil {
		return false, fmt.Errorf("archive: insert group event: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// GroupEventQuery selects group events in arrival order.
type GroupEventQuery struct {
	After int64     // only rows with seq > After
	Since time.Time // and, when non-zero, ts >= Since
	Chat  string    // one group JID; empty = all
	Limit int       // required, > 0
}

// GroupEvents returns events in the order they were stored (seq), like
// Feed does for messages.
func (s *Store) GroupEvents(ctx context.Context, q GroupEventQuery) ([]GroupEvent, error) {
	if q.Limit <= 0 {
		return nil, errors.New("archive: group events: limit must be positive")
	}
	sqlq := `SELECT e.seq, e.id, e.chat_jid, c.name, e.ts, e.action, e.member, COALESCE(e.actor, ''), COALESCE(e.via, '')
	         FROM group_events e JOIN chats c ON c.jid = e.chat_jid WHERE e.seq > ?`
	args := []any{q.After}
	if !q.Since.IsZero() {
		sqlq += ` AND e.ts >= ?`
		args = append(args, q.Since.Unix())
	}
	if q.Chat != "" {
		sqlq += ` AND e.chat_jid = ?`
		args = append(args, q.Chat)
	}
	sqlq += ` ORDER BY e.seq LIMIT ?`
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, fmt.Errorf("archive: group events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []GroupEvent
	for rows.Next() {
		var e GroupEvent
		var ts int64
		if err := rows.Scan(&e.Seq, &e.ID, &e.ChatJID, &e.ChatName, &ts, &e.Action, &e.Member, &e.Actor, &e.Via); err != nil {
			return nil, fmt.Errorf("archive: scan group event: %w", err)
		}
		e.Timestamp = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneGroupEvents deletes events older than ttl.
func (s *Store) PruneGroupEvents(ctx context.Context, ttl time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM group_events WHERE ts < ?`, s.now().Add(-ttl).Unix())
	if err != nil {
		return 0, fmt.Errorf("archive: prune group events: %w", err)
	}
	return res.RowsAffected()
}
