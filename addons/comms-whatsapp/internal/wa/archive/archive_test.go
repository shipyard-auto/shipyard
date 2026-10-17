package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func openTest(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	now := t0
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.WithClock(func() time.Time { return now })
	t.Cleanup(func() { _ = s.Close() })
	return s, &now
}

func msg(chat, id string, ts time.Time, text string) Message {
	return Message{ChatJID: chat, ID: id, SenderJID: chat, Timestamp: ts, Kind: "text", Text: text}
}

func TestOpenCreatesFile0600AndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	for i := 0; i < 2; i++ {
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		_ = s.Close()
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm=%o want 600", perm)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("want error for newer schema")
	}
}

func TestOnlyWatchedChatsGetMessages(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, "a@s.whatsapp.net", "Mãe", KindDM))
	must(t, s.UpsertChat(ctx, "b@g.us", "Família", KindGroup))
	must(t, s.Watch(ctx, "b@g.us", 0))

	if ok, err := s.InsertMessage(ctx, msg("a@s.whatsapp.net", "1", t0, "oi")); err != nil || ok {
		t.Fatalf("unwatched insert: ok=%v err=%v", ok, err)
	}
	if ok, err := s.InsertMessage(ctx, msg("b@g.us", "2", t0, "oi")); err != nil || !ok {
		t.Fatalf("watched insert: ok=%v err=%v", ok, err)
	}
	if ok, err := s.InsertMessage(ctx, msg("unknown@s.whatsapp.net", "3", t0, "oi")); err != nil || ok {
		t.Fatalf("unknown chat insert: ok=%v err=%v", ok, err)
	}
	c, err := s.Counts(ctx)
	must(t, err)
	if c != (Counts{Chats: 2, Watched: 1, Messages: 1}) {
		t.Fatalf("counts=%+v", c)
	}
}

func TestDuplicateMessageUpdatesText(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, "b@g.us", "Família", KindGroup))
	must(t, s.Watch(ctx, "b@g.us", 0))
	_, err := s.InsertMessage(ctx, msg("b@g.us", "1", t0, "v1"))
	must(t, err)
	_, err = s.InsertMessage(ctx, msg("b@g.us", "1", t0, "v2 (editada)"))
	must(t, err)
	h, err := s.History(ctx, "b@g.us", time.Time{}, 0)
	must(t, err)
	if len(h) != 1 || h[0].Text != "v2 (editada)" {
		t.Fatalf("history=%+v", h)
	}
}

func TestUnwatchDeletesMessages(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, "b@g.us", "Família", KindGroup))
	must(t, s.Watch(ctx, "b@g.us", 0))
	for _, id := range []string{"1", "2"} {
		_, err := s.InsertMessage(ctx, msg("b@g.us", id, t0, "x"))
		must(t, err)
	}
	n, err := s.Unwatch(ctx, "b@g.us")
	must(t, err)
	if n != 2 {
		t.Fatalf("deleted=%d want 2", n)
	}
	if w, _ := s.IsWatched(ctx, "b@g.us"); w {
		t.Fatal("still watched")
	}
	if _, err := s.Unwatch(ctx, "nope@g.us"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown unwatch: %v", err)
	}
}

func TestUpsertKeepsWatchAndNameOnEmpty(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, "a@s.whatsapp.net", "Mãe", KindDM))
	must(t, s.Watch(ctx, "a@s.whatsapp.net", 48*time.Hour))
	must(t, s.UpsertChat(ctx, "a@s.whatsapp.net", "", KindDM))
	c, err := s.GetChat(ctx, "a@s.whatsapp.net")
	must(t, err)
	if c.Name != "Mãe" || !c.Watched || c.TTL != 48*time.Hour {
		t.Fatalf("chat=%+v", c)
	}
	must(t, s.UpsertChat(ctx, "a@s.whatsapp.net", "Mamãe", KindDM))
	c, _ = s.GetChat(ctx, "a@s.whatsapp.net")
	if c.Name != "Mamãe" || !c.Watched {
		t.Fatalf("rename lost watch: %+v", c)
	}
	// A chat first seen without a name falls back to its JID.
	must(t, s.UpsertChat(ctx, "z@s.whatsapp.net", "", KindDM))
	c, _ = s.GetChat(ctx, "z@s.whatsapp.net")
	if c.Name != "z@s.whatsapp.net" {
		t.Fatalf("name=%q", c.Name)
	}
}

func TestResolve(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, "a@s.whatsapp.net", "Mãe", KindDM))
	must(t, s.UpsertChat(ctx, "b@g.us", "Família", KindGroup))
	must(t, s.UpsertChat(ctx, "c@g.us", "família", KindGroup))

	if c, err := s.Resolve(ctx, "a@s.whatsapp.net"); err != nil || c.Name != "Mãe" {
		t.Fatalf("by jid: %+v %v", c, err)
	}
	if c, err := s.Resolve(ctx, " Mãe "); err != nil || c.JID != "a@s.whatsapp.net" {
		t.Fatalf("by name: %+v %v", c, err)
	}
	// Case and accents are ignored, so all three spellings hit both groups.
	for _, q := range []string{"Família", "FAMILIA", "familia"} {
		var amb *AmbiguousError
		if _, err := s.Resolve(ctx, q); !errors.As(err, &amb) || len(amb.Matches) != 2 {
			t.Fatalf("%q: want ambiguous with 2 matches, got %v", q, err)
		}
	}
	if c, err := s.Resolve(ctx, "MAE"); err != nil || c.JID != "a@s.whatsapp.net" {
		t.Fatalf("accent-folded name: %+v %v", c, err)
	}
	if _, err := s.Resolve(ctx, "ninguém"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestListChatsSearchAndWatched(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, "a@s.whatsapp.net", "Pet Shop 100%", KindDM))
	must(t, s.UpsertChat(ctx, "b@g.us", "Família", KindGroup))
	must(t, s.Watch(ctx, "b@g.us", 0))

	got, err := s.ListChats(ctx, ListFilter{Search: "100%"})
	must(t, err)
	if len(got) != 1 || got[0].JID != "a@s.whatsapp.net" {
		t.Fatalf("search=%+v", got)
	}
	got, err = s.ListChats(ctx, ListFilter{Search: "FAMILIA"})
	must(t, err)
	if len(got) != 1 || got[0].JID != "b@g.us" {
		t.Fatalf("folded search=%+v", got)
	}
	got, err = s.ListChats(ctx, ListFilter{Search: "g.us"})
	must(t, err)
	if len(got) != 1 {
		t.Fatalf("jid search=%+v", got)
	}
	got, err = s.ListChats(ctx, ListFilter{WatchedOnly: true})
	must(t, err)
	if len(got) != 1 || got[0].Name != "Família" {
		t.Fatalf("watched=%+v", got)
	}
}

func TestHistoryWindowLimitAndOrder(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, "b@g.us", "Família", KindGroup))
	must(t, s.Watch(ctx, "b@g.us", 0))
	for i, id := range []string{"1", "2", "3", "4"} {
		_, err := s.InsertMessage(ctx, msg("b@g.us", id, t0.Add(time.Duration(i)*time.Hour), id))
		must(t, err)
	}
	h, err := s.History(ctx, "b@g.us", t0.Add(time.Hour), 2)
	must(t, err)
	if len(h) != 2 || h[0].ID != "3" || h[1].ID != "4" {
		t.Fatalf("history=%+v", h)
	}
	h, err = s.History(ctx, "b@g.us", t0.Add(time.Hour), 0)
	must(t, err)
	if len(h) != 3 || h[0].ID != "2" {
		t.Fatalf("unlimited=%+v", h)
	}
}

func TestPruneHonoursPerChatTTL(t *testing.T) {
	s, now := openTest(t)
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, "a@g.us", "curto", KindGroup))
	must(t, s.UpsertChat(ctx, "b@g.us", "padrão", KindGroup))
	must(t, s.Watch(ctx, "a@g.us", 24*time.Hour))
	must(t, s.Watch(ctx, "b@g.us", 0))
	old := t0.Add(-48 * time.Hour)
	for _, c := range []string{"a@g.us", "b@g.us"} {
		_, err := s.InsertMessage(ctx, msg(c, "old", old, "x"))
		must(t, err)
		_, err = s.InsertMessage(ctx, msg(c, "new", t0, "x"))
		must(t, err)
	}
	*now = t0
	n, err := s.Prune(ctx, 7*24*time.Hour)
	must(t, err)
	if n != 1 {
		t.Fatalf("pruned=%d want 1 (only the 24h chat's old row)", n)
	}
	*now = t0.Add(8 * 24 * time.Hour)
	n, err = s.Prune(ctx, 7*24*time.Hour)
	must(t, err)
	if n != 3 {
		t.Fatalf("pruned=%d want 3", n)
	}
}

func TestConcurrentWritersAcrossHandles(t *testing.T) {
	// The CLI and the daemon open the same file; WAL + busy_timeout must
	// serialize them without "database is locked".
	path := filepath.Join(t.TempDir(), "archive.db")
	ctx := context.Background()
	a, err := Open(ctx, path)
	must(t, err)
	defer func() { _ = a.Close() }()
	b, err := Open(ctx, path)
	must(t, err)
	defer func() { _ = b.Close() }()
	must(t, a.UpsertChat(ctx, "b@g.us", "Família", KindGroup))
	must(t, a.Watch(ctx, "b@g.us", 0))

	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_, err := a.InsertMessage(ctx, msg("b@g.us", "a"+itoa(i), t0, "x"))
			errs <- err
		}(i)
		go func(i int) {
			defer wg.Done()
			errs <- b.UpsertChat(ctx, "c"+itoa(i)+"@g.us", "g", KindGroup)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrationFromV1KeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	ctx := context.Background()
	// Build a v1 database by hand: only the first migration applied.
	s, err := Open(ctx, path)
	must(t, err)
	_ = s.Close()
	raw, err := Open(ctx, path)
	must(t, err)
	if _, err := raw.db.Exec(`DROP TABLE sent; DROP INDEX IF EXISTS sent_ts;`); err != nil {
		t.Fatal(err)
	}
	// SQLite cannot drop the added columns portably; rebuild v1 tables.
	if _, err := raw.db.Exec(`
		DROP TABLE messages; DROP TABLE chats;
		CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('dm','group','self')),
			watched INTEGER NOT NULL DEFAULT 0, ttl_seconds INTEGER, updated_at INTEGER NOT NULL);
		CREATE TABLE messages (chat_jid TEXT NOT NULL REFERENCES chats(jid) ON DELETE CASCADE,
			msg_id TEXT NOT NULL, sender_jid TEXT NOT NULL, sender_name TEXT, from_me INTEGER NOT NULL,
			ts INTEGER NOT NULL, kind TEXT NOT NULL, text TEXT, reply_to TEXT, PRIMARY KEY (chat_jid, msg_id));
		INSERT INTO chats VALUES ('b@g.us', 'Família', 'group', 1, NULL, 0);
		INSERT INTO messages VALUES ('b@g.us', '1', 'x', NULL, 0, 100, 'text', 'oi', NULL);
		PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	s, err = Open(ctx, path)
	must(t, err)
	defer func() { _ = s.Close() }()
	c, err := s.GetChat(ctx, "b@g.us")
	if err != nil || !c.Watched || c.SendAllowed || !c.LastActivity.IsZero() || c.Messages != 1 {
		t.Fatalf("migrated chat=%+v err=%v", c, err)
	}
	h, _ := s.History(ctx, "b@g.us", time.Time{}, 0)
	if len(h) != 1 || h[0].ViaShipyard {
		t.Fatalf("migrated history=%+v", h)
	}
}

func TestAllowlistActivityAndSentLog(t *testing.T) {
	s, now := openTest(t)
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, "a@s.whatsapp.net", "Vini", KindDM))

	must(t, s.SetSendAllowed(ctx, "a@s.whatsapp.net", true))
	if err := s.SetSendAllowed(ctx, "nope@s.whatsapp.net", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown allow: %v", err)
	}

	must(t, s.TouchActivity(ctx, "a@s.whatsapp.net", t0))
	must(t, s.TouchActivity(ctx, "a@s.whatsapp.net", t0.Add(-time.Hour))) // older: ignored
	must(t, s.TouchActivity(ctx, "unknown@s.whatsapp.net", t0))           // unknown: no-op
	c, _ := s.GetChat(ctx, "a@s.whatsapp.net")
	if !c.SendAllowed || !c.LastActivity.Equal(t0) {
		t.Fatalf("chat=%+v", c)
	}

	must(t, s.RecordSent(ctx, Sent{MsgID: "m1", ChatJID: "a@s.whatsapp.net", Timestamp: t0, Chars: 3, IdempotencyKey: "k1"}))
	must(t, s.RecordSent(ctx, Sent{MsgID: "m2", ChatJID: "b@s.whatsapp.net", Timestamp: t0.Add(-2 * time.Hour), Chars: 3}))
	if err := s.RecordSent(ctx, Sent{MsgID: "m3", ChatJID: "a@s.whatsapp.net", Timestamp: t0, IdempotencyKey: "k1"}); err == nil {
		t.Fatal("duplicate idempotency key accepted")
	}
	if n, _ := s.CountSent(ctx, "a@s.whatsapp.net", t0.Add(-time.Minute)); n != 1 {
		t.Fatalf("per chat=%d", n)
	}
	if n, _ := s.CountSent(ctx, "", t0.Add(-3*time.Hour)); n != 2 {
		t.Fatalf("global=%d", n)
	}
	got, ok, err := s.SentByKey(ctx, "k1")
	if err != nil || !ok || got.MsgID != "m1" {
		t.Fatalf("by key=%+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.SentByKey(ctx, "missing"); ok {
		t.Fatal("missing key found")
	}
	counts, _ := s.Counts(ctx)
	if counts.SendAllowed != 1 || counts.SentLastHour != 1 {
		t.Fatalf("counts=%+v", counts)
	}

	*now = t0.Add(SentRetention + time.Hour)
	_, err = s.Prune(ctx, defaultTestTTL)
	must(t, err)
	if n, _ := s.CountSent(ctx, "", time.Time{}); n != 0 {
		t.Fatalf("sent log not pruned: %d", n)
	}
}

const defaultTestTTL = 14 * 24 * time.Hour
