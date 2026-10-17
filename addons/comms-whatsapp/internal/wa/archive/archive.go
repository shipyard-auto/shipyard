// Package archive is the local SQLite store behind shipyard-comms-whatsapp:
// the catalog of known chats (with per-chat watch/TTL settings) and the
// messages of watched chats.
//
// Only chats with watched = 1 ever get message rows; the check happens
// inside the INSERT itself so a concurrent `unwatch` cannot race a write.
// Rows older than the chat's TTL are removed by Prune.
//
// The driver is modernc.org/sqlite (pure Go): releases are cross-compiled
// for four targets from one runner with CGO disabled.
package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ChatKind classifies a chat.
type ChatKind string

const (
	KindDM    ChatKind = "dm"
	KindGroup ChatKind = "group"
	KindSelf  ChatKind = "self" // "Message yourself"
)

// Chat is one row of the chat catalog.
type Chat struct {
	JID       string        `json:"jid"`
	Name      string        `json:"name"`
	Kind      ChatKind      `json:"kind"`
	Watched   bool          `json:"watched"`
	TTL       time.Duration `json:"-"` // 0 = config default_ttl
	Messages  int           `json:"messages"`
	UpdatedAt time.Time     `json:"updated_at"`
	// SendAllowed puts the chat on the send allowlist.
	SendAllowed bool `json:"send_allowed"`
	// LastActivity is the newest message seen in either direction, watched
	// or not (no content is kept for unwatched chats — just this stamp).
	// Zero means no conversation was ever observed.
	LastActivity time.Time `json:"last_activity_at,omitzero"`
}

// Message is one archived message.
type Message struct {
	ChatJID    string    `json:"chat_jid"`
	ID         string    `json:"id"`
	SenderJID  string    `json:"sender_jid"`
	SenderName string    `json:"sender_name,omitempty"`
	FromMe     bool      `json:"from_me"`
	Timestamp  time.Time `json:"ts"`
	Kind       string    `json:"kind"` // text|image|audio|video|file|location|sticker|contact|poll|other
	Text       string    `json:"text,omitempty"`
	ReplyTo    string    `json:"reply_to,omitempty"`
	// ViaShipyard marks messages this daemon sent, as opposed to ones the
	// user typed on the phone.
	ViaShipyard bool `json:"via_shipyard,omitempty"`
}

// Sent is one row of the send audit log. It is written for every message
// the daemon sends, watched chat or not, and is what rate limits count —
// so a crash-looping daemon cannot reset its own counters.
type Sent struct {
	MsgID          string    `json:"msg_id"`
	ChatJID        string    `json:"chat_jid"`
	Timestamp      time.Time `json:"ts"`
	Chars          int       `json:"chars"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	// Client is the external system that asked for the send (its fairway
	// client id); empty for local senders (CLI, crew tools).
	Client string `json:"client,omitempty"`
}

// SentRetention is how long the send audit log is kept.
const SentRetention = 30 * 24 * time.Hour

// ErrNotFound is returned when a chat lookup matches nothing.
var ErrNotFound = errors.New("chat not found")

// AmbiguousError is returned by Resolve when a name matches several chats.
type AmbiguousError struct {
	Query   string
	Matches []Chat
}

func (e *AmbiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d chats; use the JID:", e.Query, len(e.Matches))
	for _, c := range e.Matches {
		fmt.Fprintf(&b, "\n  %s  %s (%s)", c.JID, c.Name, c.Kind)
	}
	return b.String()
}

// Store wraps the archive database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// migrations are applied in order; PRAGMA user_version records how many ran.
var migrations = []string{
	`CREATE TABLE chats (
		jid          TEXT PRIMARY KEY,
		name         TEXT NOT NULL,
		kind         TEXT NOT NULL CHECK (kind IN ('dm','group','self')),
		watched      INTEGER NOT NULL DEFAULT 0,
		ttl_seconds  INTEGER,
		updated_at   INTEGER NOT NULL
	);
	CREATE TABLE messages (
		chat_jid     TEXT NOT NULL REFERENCES chats(jid) ON DELETE CASCADE,
		msg_id       TEXT NOT NULL,
		sender_jid   TEXT NOT NULL,
		sender_name  TEXT,
		from_me      INTEGER NOT NULL,
		ts           INTEGER NOT NULL,
		kind         TEXT NOT NULL,
		text         TEXT,
		reply_to     TEXT,
		PRIMARY KEY (chat_jid, msg_id)
	);
	CREATE INDEX messages_chat_ts ON messages(chat_jid, ts);`,

	// v2: sending. Allowlist and last-activity per chat, provenance of
	// archived messages, and the send audit log.
	`ALTER TABLE chats ADD COLUMN send_allowed INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE chats ADD COLUMN last_activity_at INTEGER;
	ALTER TABLE messages ADD COLUMN via_shipyard INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE sent (
		msg_id           TEXT PRIMARY KEY,
		chat_jid         TEXT NOT NULL,
		ts               INTEGER NOT NULL,
		chars            INTEGER NOT NULL,
		idempotency_key  TEXT UNIQUE
	);
	CREATE INDEX sent_ts ON sent(ts);
	CREATE INDEX sent_chat_ts ON sent(chat_jid, ts);`,

	// v3: a stable arrival sequence for cursor reads. seq is AUTOINCREMENT
	// so it is never reused, even after pruning or deleting the newest row
	// (a reused number would hide a message from a reader parked on it).
	// Existing rows are numbered in timestamp order.
	`CREATE TABLE messages_v3 (
		seq          INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_jid     TEXT NOT NULL REFERENCES chats(jid) ON DELETE CASCADE,
		msg_id       TEXT NOT NULL,
		sender_jid   TEXT NOT NULL,
		sender_name  TEXT,
		from_me      INTEGER NOT NULL,
		ts           INTEGER NOT NULL,
		kind         TEXT NOT NULL,
		text         TEXT,
		reply_to     TEXT,
		via_shipyard INTEGER NOT NULL DEFAULT 0,
		UNIQUE (chat_jid, msg_id)
	);
	INSERT INTO messages_v3 (chat_jid, msg_id, sender_jid, sender_name, from_me, ts, kind, text, reply_to, via_shipyard)
		SELECT chat_jid, msg_id, sender_jid, sender_name, from_me, ts, kind, text, reply_to, via_shipyard
		FROM messages ORDER BY ts, msg_id;
	DROP TABLE messages;
	ALTER TABLE messages_v3 RENAME TO messages;
	CREATE INDEX messages_chat_ts ON messages(chat_jid, ts);`,

	// v4: who asked for each send (external client id; NULL = local).
	`ALTER TABLE sent ADD COLUMN client TEXT;`,
}

// Open opens (creating if needed) the archive at path with mode 0600 and
// applies pending migrations. The CLI and the daemon may hold it at the
// same time: WAL mode plus a busy timeout serializes their writes.
func Open(ctx context.Context, path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("archive: create %s: %w", path, err)
	}
	_ = f.Close()
	// _txlock=immediate takes the write lock at BEGIN, so a transaction
	// never fails mid-way upgrading a read lock (which skips busy_timeout).
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("archive: open %s: %w", path, err)
	}
	// One connection per handle: writers inside a process queue on the
	// pool instead of contending for SQLite's lock; contention between
	// processes (CLI vs daemon) is left to busy_timeout.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// WithClock overrides the clock used for updated_at and Prune (tests).
func (s *Store) WithClock(now func() time.Time) *Store {
	s.now = now
	return s
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("archive: read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("archive: schema version %d is newer than this binary supports (%d); upgrade shipyard-comms-whatsapp", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("archive: migrate: %w", err)
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("archive: migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("archive: migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("archive: migration %d: %w", i+1, err)
		}
	}
	return nil
}

// UpsertChat inserts the chat or refreshes its name and kind. It never
// touches watched or ttl. An empty name keeps the stored one.
func (s *Store) UpsertChat(ctx context.Context, jid, name string, kind ChatKind) error {
	if name == "" {
		name = jid
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO chats (jid, name, kind, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (jid) DO UPDATE SET
			name = CASE WHEN excluded.name = excluded.jid THEN chats.name ELSE excluded.name END,
			kind = excluded.kind,
			updated_at = CASE WHEN chats.name = excluded.name AND chats.kind = excluded.kind
			                  THEN chats.updated_at ELSE excluded.updated_at END`,
		jid, name, string(kind), s.now().Unix())
	if err != nil {
		return fmt.Errorf("archive: upsert chat %s: %w", jid, err)
	}
	return nil
}

// Watch turns archiving on for jid. ttl 0 means "use the config default".
func (s *Store) Watch(ctx context.Context, jid string, ttl time.Duration) error {
	var ttlArg any
	if ttl > 0 {
		ttlArg = int64(ttl / time.Second)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE chats SET watched = 1, ttl_seconds = ?, updated_at = ? WHERE jid = ?`,
		ttlArg, s.now().Unix(), jid)
	if err != nil {
		return fmt.Errorf("archive: watch %s: %w", jid, err)
	}
	return requireRow(res, jid)
}

// Unwatch turns archiving off for jid and deletes its archived messages:
// stopping observation of a chat should not leave its history behind.
// Returns how many messages were deleted.
func (s *Store) Unwatch(ctx context.Context, jid string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("archive: unwatch %s: %w", jid, err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE chats SET watched = 0, ttl_seconds = NULL, updated_at = ? WHERE jid = ?`,
		s.now().Unix(), jid)
	if err != nil {
		return 0, fmt.Errorf("archive: unwatch %s: %w", jid, err)
	}
	if err := requireRow(res, jid); err != nil {
		return 0, err
	}
	del, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE chat_jid = ?`, jid)
	if err != nil {
		return 0, fmt.Errorf("archive: unwatch %s: %w", jid, err)
	}
	n, _ := del.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("archive: unwatch %s: %w", jid, err)
	}
	return n, nil
}

func requireRow(res sql.Result, jid string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, jid)
	}
	return nil
}

// IsWatched reports whether jid is watched (false when unknown).
func (s *Store) IsWatched(ctx context.Context, jid string) (bool, error) {
	var w int
	err := s.db.QueryRowContext(ctx, `SELECT watched FROM chats WHERE jid = ?`, jid).Scan(&w)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("archive: %w", err)
	}
	return w == 1, nil
}

// Known reports whether jid is in the catalog.
func (s *Store) Known(ctx context.Context, jid string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM chats WHERE jid = ?`, jid).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("archive: %w", err)
	}
	return true, nil
}

// ListFilter narrows ListChats.
type ListFilter struct {
	Search      string // case-insensitive substring of name or JID
	WatchedOnly bool
}

const chatColumns = `c.jid, c.name, c.kind, c.watched, COALESCE(c.ttl_seconds, 0), c.updated_at,
	(SELECT COUNT(*) FROM messages m WHERE m.chat_jid = c.jid),
	c.send_allowed, COALESCE(c.last_activity_at, 0)`

// ListChats returns chats ordered by name. Search ignores case and
// accents ("familia" finds "Família"); the catalog is small enough to
// filter in Go, where that folding is easy.
func (s *Store) ListChats(ctx context.Context, f ListFilter) ([]Chat, error) {
	q := `SELECT ` + chatColumns + ` FROM chats c`
	if f.WatchedOnly {
		q += ` WHERE c.watched = 1`
	}
	q += ` ORDER BY lower(c.name), c.jid`
	chats, err := s.queryChats(ctx, q)
	if err != nil || f.Search == "" {
		return chats, err
	}
	needle := Fold(f.Search)
	out := chats[:0]
	for _, c := range chats {
		if strings.Contains(Fold(c.Name), needle) || strings.Contains(c.JID, needle) {
			out = append(out, c)
		}
	}
	return out, nil
}

// GetChat returns the chat with this JID.
func (s *Store) GetChat(ctx context.Context, jid string) (Chat, error) {
	chats, err := s.queryChats(ctx, `SELECT `+chatColumns+` FROM chats c WHERE c.jid = ?`, jid)
	if err != nil {
		return Chat{}, err
	}
	if len(chats) == 0 {
		return Chat{}, fmt.Errorf("%w: %s", ErrNotFound, jid)
	}
	return chats[0], nil
}

// Resolve finds a chat by exact JID, then by name ignoring case and
// accents. Several chats sharing a name yield *AmbiguousError.
func (s *Store) Resolve(ctx context.Context, query string) (Chat, error) {
	query = strings.TrimSpace(query)
	if c, err := s.GetChat(ctx, query); err == nil {
		return c, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Chat{}, err
	}
	all, err := s.ListChats(ctx, ListFilter{})
	if err != nil {
		return Chat{}, err
	}
	want := Fold(query)
	var matches []Chat
	for _, c := range all {
		if Fold(c.Name) == want {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return Chat{}, fmt.Errorf("%w: %q (try 'chats --search')", ErrNotFound, query)
	case 1:
		return matches[0], nil
	default:
		return Chat{}, &AmbiguousError{Query: query, Matches: matches}
	}
}

// Fold normalizes a name for matching: trimmed, lower-case, without
// diacritics.
func Fold(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	return strings.ToLower(strings.TrimSpace(out))
}

func (s *Store) queryChats(ctx context.Context, q string, args ...any) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("archive: list chats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Chat
	for rows.Next() {
		var c Chat
		var kind string
		var watched, allowed int
		var ttl, updated, activity int64
		if err := rows.Scan(&c.JID, &c.Name, &kind, &watched, &ttl, &updated, &c.Messages, &allowed, &activity); err != nil {
			return nil, fmt.Errorf("archive: scan chat: %w", err)
		}
		c.Kind = ChatKind(kind)
		c.Watched = watched == 1
		c.SendAllowed = allowed == 1
		if activity > 0 {
			c.LastActivity = time.Unix(activity, 0).UTC()
		}
		c.TTL = time.Duration(ttl) * time.Second
		c.UpdatedAt = time.Unix(updated, 0).UTC()
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("archive: list chats: %w", err)
	}
	return out, nil
}

// InsertMessage stores m if, and only if, its chat is watched. Duplicate
// IDs (redelivery, edits) overwrite the text. Returns whether a row was
// written.
func (s *Store) InsertMessage(ctx context.Context, m Message) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (chat_jid, msg_id, sender_jid, sender_name, from_me, ts, kind, text, reply_to, via_shipyard)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM chats WHERE jid = ? AND watched = 1)
		ON CONFLICT (chat_jid, msg_id) DO UPDATE SET text = excluded.text, kind = excluded.kind`,
		m.ChatJID, m.ID, m.SenderJID, nullable(m.SenderName), boolInt(m.FromMe), m.Timestamp.Unix(),
		m.Kind, nullable(m.Text), nullable(m.ReplyTo), boolInt(m.ViaShipyard), m.ChatJID)
	if err != nil {
		return false, fmt.Errorf("archive: insert message %s/%s: %w", m.ChatJID, m.ID, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// EditMessage replaces the content of an archived message. A message that
// was never archived (chat not watched at the time) stays absent.
func (s *Store) EditMessage(ctx context.Context, chatJID, id, kind, text string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET kind = ?, text = ? WHERE chat_jid = ? AND msg_id = ?`,
		kind, nullable(text), chatJID, id)
	if err != nil {
		return fmt.Errorf("archive: edit message %s/%s: %w", chatJID, id, err)
	}
	return nil
}

// DeleteMessage removes an archived message ("delete for everyone").
func (s *Store) DeleteMessage(ctx context.Context, chatJID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE chat_jid = ? AND msg_id = ?`, chatJID, id)
	if err != nil {
		return fmt.Errorf("archive: delete message %s/%s: %w", chatJID, id, err)
	}
	return nil
}

// History returns the newest `limit` messages of jid at or after since,
// oldest first. limit <= 0 means no limit.
func (s *Store) History(ctx context.Context, jid string, since time.Time, limit int) ([]Message, error) {
	q := `SELECT chat_jid, msg_id, sender_jid, COALESCE(sender_name, ''), from_me, ts, kind,
	             COALESCE(text, ''), COALESCE(reply_to, ''), via_shipyard
	      FROM messages WHERE chat_jid = ? AND ts >= ? ORDER BY ts DESC, msg_id DESC`
	args := []any{jid, since.Unix()}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("archive: history %s: %w", jid, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Message
	for rows.Next() {
		var m Message
		var fromMe, via int
		var ts int64
		if err := rows.Scan(&m.ChatJID, &m.ID, &m.SenderJID, &m.SenderName, &fromMe, &ts, &m.Kind, &m.Text, &m.ReplyTo, &via); err != nil {
			return nil, fmt.Errorf("archive: scan message: %w", err)
		}
		m.FromMe = fromMe == 1
		m.ViaShipyard = via == 1
		m.Timestamp = time.Unix(ts, 0).UTC()
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("archive: history %s: %w", jid, err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Prune deletes messages older than their chat's TTL (defaultTTL for chats
// without one) and send-log rows older than SentRetention. Returns how
// many messages were removed.
func (s *Store) Prune(ctx context.Context, defaultTTL time.Duration) (int64, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM messages WHERE ts < ? - COALESCE(
			(SELECT ttl_seconds FROM chats c WHERE c.jid = messages.chat_jid), ?)`,
		now.Unix(), int64(defaultTTL/time.Second))
	if err != nil {
		return 0, fmt.Errorf("archive: prune: %w", err)
	}
	n, _ := res.RowsAffected()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sent WHERE ts < ?`, now.Add(-SentRetention).Unix()); err != nil {
		return n, fmt.Errorf("archive: prune sent: %w", err)
	}
	return n, nil
}

// SetSendAllowed adds jid to, or removes it from, the send allowlist.
func (s *Store) SetSendAllowed(ctx context.Context, jid string, allowed bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE chats SET send_allowed = ?, updated_at = ? WHERE jid = ?`,
		boolInt(allowed), s.now().Unix(), jid)
	if err != nil {
		return fmt.Errorf("archive: allow %s: %w", jid, err)
	}
	return requireRow(res, jid)
}

// TouchActivity records that a message was seen in jid at ts. The stamp
// only moves forward; unknown chats are ignored.
func (s *Store) TouchActivity(ctx context.Context, jid string, ts time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE chats SET last_activity_at = MAX(COALESCE(last_activity_at, 0), ?) WHERE jid = ?`,
		ts.Unix(), jid)
	if err != nil {
		return fmt.Errorf("archive: touch %s: %w", jid, err)
	}
	return nil
}

// RecordSent appends to the send audit log.
func (s *Store) RecordSent(ctx context.Context, r Sent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sent (msg_id, chat_jid, ts, chars, idempotency_key, client) VALUES (?, ?, ?, ?, ?, ?)`,
		r.MsgID, r.ChatJID, r.Timestamp.Unix(), r.Chars, nullable(r.IdempotencyKey), nullable(r.Client))
	if err != nil {
		return fmt.Errorf("archive: record sent %s: %w", r.MsgID, err)
	}
	return nil
}

// SentByKey returns the send recorded under an idempotency key.
func (s *Store) SentByKey(ctx context.Context, key string) (Sent, bool, error) {
	var r Sent
	var ts int64
	err := s.db.QueryRowContext(ctx,
		`SELECT msg_id, chat_jid, ts, chars, COALESCE(idempotency_key, ''), COALESCE(client, '') FROM sent WHERE idempotency_key = ?`, key,
	).Scan(&r.MsgID, &r.ChatJID, &ts, &r.Chars, &r.IdempotencyKey, &r.Client)
	if errors.Is(err, sql.ErrNoRows) {
		return Sent{}, false, nil
	}
	if err != nil {
		return Sent{}, false, fmt.Errorf("archive: sent by key: %w", err)
	}
	r.Timestamp = time.Unix(ts, 0).UTC()
	return r, true, nil
}

// OldestSentSince returns the timestamp of the oldest send at or after
// since, in one chat or (chatJID "") across all chats. It tells when a
// full rate-limit window frees its next slot.
func (s *Store) OldestSentSince(ctx context.Context, chatJID string, since time.Time) (time.Time, bool, error) {
	q := `SELECT MIN(ts) FROM sent WHERE ts >= ?`
	args := []any{since.Unix()}
	if chatJID != "" {
		q += ` AND chat_jid = ?`
		args = append(args, chatJID)
	}
	var ts sql.NullInt64
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&ts); err != nil {
		return time.Time{}, false, fmt.Errorf("archive: oldest sent: %w", err)
	}
	if !ts.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(ts.Int64, 0).UTC(), true, nil
}

// CountSent counts sends at or after since, in one chat or (chatJID "")
// across all chats.
func (s *Store) CountSent(ctx context.Context, chatJID string, since time.Time) (int, error) {
	q := `SELECT COUNT(*) FROM sent WHERE ts >= ?`
	args := []any{since.Unix()}
	if chatJID != "" {
		q += ` AND chat_jid = ?`
		args = append(args, chatJID)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("archive: count sent: %w", err)
	}
	return n, nil
}

// Counts summarizes the archive for `status`.
type Counts struct {
	Chats        int `json:"chats"`
	Watched      int `json:"watched"`
	Messages     int `json:"messages"`
	SendAllowed  int `json:"send_allowed"`
	SentLastHour int `json:"sent_last_hour"`
}

// Counts returns catalog and archive sizes.
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM chats),
		(SELECT COUNT(*) FROM chats WHERE watched = 1),
		(SELECT COUNT(*) FROM messages),
		(SELECT COUNT(*) FROM chats WHERE send_allowed = 1),
		(SELECT COUNT(*) FROM sent WHERE ts >= ?)`, s.now().Add(-time.Hour).Unix(),
	).Scan(&c.Chats, &c.Watched, &c.Messages, &c.SendAllowed, &c.SentLastHour)
	if err != nil {
		return Counts{}, fmt.Errorf("archive: counts: %w", err)
	}
	return c, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FeedQuery selects archived messages in arrival order.
type FeedQuery struct {
	After int64     // only rows with seq > After
	Since time.Time // and, when non-zero, ts >= Since
	Chat  string    // one chat JID; empty = all chats
	Limit int       // rows to return (required, > 0)
}

// FeedRow is one message of a feed, with its sequence and chat.
type FeedRow struct {
	Seq      int64
	ChatName string
	ChatKind ChatKind
	Message
}

// Feed returns messages in the order they were archived (seq), which is
// not always timestamp order: a history sync can archive old messages
// late. Readers resume from the last Seq they saw.
func (s *Store) Feed(ctx context.Context, q FeedQuery) ([]FeedRow, error) {
	if q.Limit <= 0 {
		return nil, errors.New("archive: feed: limit must be positive")
	}
	sqlq := `SELECT m.seq, c.name, c.kind, m.chat_jid, m.msg_id, m.sender_jid, COALESCE(m.sender_name, ''), m.from_me, m.ts,
	                m.kind, COALESCE(m.text, ''), COALESCE(m.reply_to, ''), m.via_shipyard
	         FROM messages m JOIN chats c ON c.jid = m.chat_jid
	         WHERE m.seq > ?`
	args := []any{q.After}
	if !q.Since.IsZero() {
		sqlq += ` AND m.ts >= ?`
		args = append(args, q.Since.Unix())
	}
	if q.Chat != "" {
		sqlq += ` AND m.chat_jid = ?`
		args = append(args, q.Chat)
	}
	sqlq += ` ORDER BY m.seq LIMIT ?`
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, fmt.Errorf("archive: feed: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []FeedRow
	for rows.Next() {
		var r FeedRow
		var fromMe, via int
		var ts int64
		if err := rows.Scan(&r.Seq, &r.ChatName, &r.ChatKind, &r.ChatJID, &r.ID, &r.SenderJID, &r.SenderName, &fromMe, &ts,
			&r.Kind, &r.Text, &r.ReplyTo, &via); err != nil {
			return nil, fmt.Errorf("archive: scan feed: %w", err)
		}
		r.FromMe = fromMe == 1
		r.ViaShipyard = via == 1
		r.Timestamp = time.Unix(ts, 0).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("archive: feed: %w", err)
	}
	return out, nil
}

// SentEntry is one audit row with its chat's name.
type SentEntry struct {
	Sent
	ChatName string `json:"chat_name"`
}

// ListSent returns the send audit log since `since`, newest first.
// client filters by requester: "" = everyone, "local" = local senders only.
func (s *Store) ListSent(ctx context.Context, since time.Time, client string, limit int) ([]SentEntry, error) {
	q := `SELECT s.msg_id, s.chat_jid, COALESCE(c.name, s.chat_jid), s.ts, s.chars, COALESCE(s.idempotency_key, ''), COALESCE(s.client, '')
	      FROM sent s LEFT JOIN chats c ON c.jid = s.chat_jid WHERE s.ts >= ?`
	args := []any{since.Unix()}
	switch client {
	case "":
	case "local":
		q += ` AND s.client IS NULL`
	default:
		q += ` AND s.client = ?`
		args = append(args, client)
	}
	q += ` ORDER BY s.ts DESC, s.rowid DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("archive: list sent: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SentEntry
	for rows.Next() {
		var e SentEntry
		var ts int64
		if err := rows.Scan(&e.MsgID, &e.ChatJID, &e.ChatName, &ts, &e.Chars, &e.IdempotencyKey, &e.Client); err != nil {
			return nil, fmt.Errorf("archive: scan sent: %w", err)
		}
		e.Timestamp = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
