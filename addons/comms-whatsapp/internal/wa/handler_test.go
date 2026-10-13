package wa

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

var (
	ownPN   = types.NewJID("5511900000000", types.DefaultUserServer)
	ownLID  = types.NewJID("99000000000001", types.HiddenUserServer)
	momPN   = types.NewJID("5511911111111", types.DefaultUserServer)
	momLID  = types.NewJID("99000000000002", types.HiddenUserServer)
	shopPN  = types.NewJID("5511922222222", types.DefaultUserServer)
	shopLID = types.NewJID("99000000000003", types.HiddenUserServer)
	family  = types.NewJID("120363000000000001", types.GroupServer)
	ts      = time.Date(2026, 10, 13, 14, 2, 0, 0, time.UTC)
)

type fakeDir struct {
	lids     map[types.JID]types.JID
	contacts map[types.JID]string
	groups   map[types.JID]string
}

func newFakeDir() *fakeDir {
	return &fakeDir{
		lids:     map[types.JID]types.JID{momLID: momPN, ownLID: ownPN},
		contacts: map[types.JID]string{momPN: "Mãe", shopLID: "Loja (sem número)"},
		groups:   map[types.JID]string{family: "Família"},
	}
}

func (d *fakeDir) Own() (types.JID, types.JID) { return ownPN, ownLID }
func (d *fakeDir) OwnPushName() string         { return "Leo" }
func (d *fakeDir) PNForLID(_ context.Context, lid types.JID) (types.JID, bool) {
	pn, ok := d.lids[lid]
	return pn, ok
}
func (d *fakeDir) ContactName(_ context.Context, j types.JID) string      { return d.contacts[j] }
func (d *fakeDir) GroupName(_ context.Context, j types.JID) string        { return d.groups[j] }
func (d *fakeDir) Contacts(context.Context) (map[types.JID]string, error) { return d.contacts, nil }
func (d *fakeDir) Groups(context.Context) (map[types.JID]string, error)   { return d.groups, nil }

type harness struct {
	h     *Handler
	arch  *archive.Store
	fatal []error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	arch, err := archive.Open(context.Background(), filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = arch.Close() })
	hs := &harness{arch: arch}
	hs.h = &Handler{
		Archive: arch,
		Dir:     newFakeDir(),
		Tracker: NewTracker(filepath.Join(dir, "status.json"), 1, nil),
		Log:     slog.New(yardlogs.NopHandler()),
		OnFatal: func(err error) { hs.fatal = append(hs.fatal, err) },
		Async:   func(f func()) { f() },
	}
	return hs
}

func (hs *harness) watch(t *testing.T, jid types.JID, name string, kind archive.ChatKind) {
	t.Helper()
	ctx := context.Background()
	if err := hs.arch.UpsertChat(ctx, jid.String(), name, kind); err != nil {
		t.Fatal(err)
	}
	if err := hs.arch.Watch(ctx, jid.String(), 0); err != nil {
		t.Fatal(err)
	}
}

func (hs *harness) history(t *testing.T, jid types.JID) []archive.Message {
	t.Helper()
	h, err := hs.arch.History(context.Background(), jid.String(), time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func text(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

func message(src types.MessageSource, id string, m *waE2E.Message) *events.Message {
	return &events.Message{
		Info:    types.MessageInfo{MessageSource: src, ID: id, Timestamp: ts, PushName: "push"},
		Message: m,
	}
}

func TestGroupMessageArchivedWithSenderName(t *testing.T) {
	hs := newHarness(t)
	hs.watch(t, family, "Família", archive.KindGroup)
	hs.h.Handle(context.Background(), message(types.MessageSource{
		Chat: family, Sender: momLID, SenderAlt: momPN, IsGroup: true,
	}, "m1", text("almoço domingo?")))

	got := hs.history(t, family)
	if len(got) != 1 {
		t.Fatalf("history=%+v", got)
	}
	m := got[0]
	if m.SenderJID != momPN.String() || m.SenderName != "Mãe" || m.Text != "almoço domingo?" || m.FromMe {
		t.Fatalf("message=%+v", m)
	}
}

func TestDMByLIDIsFiledUnderPhoneNumber(t *testing.T) {
	cases := []struct {
		name string
		src  types.MessageSource
	}{
		{"alt address", types.MessageSource{Chat: momLID, Sender: momLID, SenderAlt: momPN}},
		{"lid store", types.MessageSource{Chat: momLID, Sender: momLID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := newHarness(t)
			hs.watch(t, momPN, "Mãe", archive.KindDM)
			hs.h.Handle(context.Background(), message(tc.src, "m1", text("oi filho")))
			if got := hs.history(t, momPN); len(got) != 1 || got[0].SenderJID != momPN.String() {
				t.Fatalf("history=%+v", got)
			}
		})
	}
}

func TestOwnMessageInDMUsesRecipientAlt(t *testing.T) {
	hs := newHarness(t)
	hs.watch(t, shopPN, "Petz", archive.KindDM)
	hs.h.Handle(context.Background(), message(types.MessageSource{
		Chat: shopLID, Sender: ownLID, SenderAlt: ownPN, RecipientAlt: shopPN, IsFromMe: true,
	}, "m1", text("queria ração")))
	got := hs.history(t, shopPN)
	if len(got) != 1 || !got[0].FromMe || got[0].SenderJID != ownPN.String() || got[0].SenderName != "" {
		t.Fatalf("history=%+v", got)
	}
}

func TestUnmappableLIDKeepsLID(t *testing.T) {
	hs := newHarness(t)
	stranger := types.NewJID("99000000000009", types.HiddenUserServer)
	hs.h.Handle(context.Background(), message(types.MessageSource{Chat: stranger, Sender: stranger}, "m1", text("oi")))
	c, err := hs.arch.GetChat(context.Background(), stranger.String())
	if err != nil || c.Kind != archive.KindDM || c.Name != "push" {
		t.Fatalf("chat=%+v err=%v", c, err)
	}
}

func TestSelfChat(t *testing.T) {
	hs := newHarness(t)
	hs.watch(t, ownPN, "me", archive.KindSelf)
	hs.h.Handle(context.Background(), message(types.MessageSource{
		Chat: ownLID, Sender: ownLID, IsFromMe: true,
	}, "m1", text("nota pra mim")))
	if got := hs.history(t, ownPN); len(got) != 1 {
		t.Fatalf("self history=%+v", got)
	}
	c, _ := hs.arch.GetChat(context.Background(), ownPN.String())
	if c.Kind != archive.KindSelf {
		t.Fatalf("kind=%s", c.Kind)
	}
}

func TestUntrackedChatsIgnored(t *testing.T) {
	hs := newHarness(t)
	for _, chat := range []types.JID{
		types.StatusBroadcastJID,
		types.NewJID("123", types.BroadcastServer),
		types.NewJID("123", types.NewsletterServer),
	} {
		hs.h.Handle(context.Background(), message(types.MessageSource{Chat: chat, Sender: momPN}, "x", text("hi")))
	}
	if c, _ := hs.arch.Counts(context.Background()); c.Chats != 0 {
		t.Fatalf("untracked chats catalogued: %+v", c)
	}
}

func TestFirstMessageCataloguesChatButArchivesOnlyWhenWatched(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	hs.h.Handle(ctx, message(types.MessageSource{Chat: family, Sender: momPN, IsGroup: true}, "m1", text("oi")))
	c, err := hs.arch.GetChat(ctx, family.String())
	if err != nil || c.Name != "Família" || c.Kind != archive.KindGroup || c.Watched || c.Messages != 0 {
		t.Fatalf("chat=%+v err=%v", c, err)
	}
	// Own message to an unknown contact: no push name to borrow.
	hs.h.Handle(ctx, message(types.MessageSource{Chat: shopPN, Sender: ownPN, IsFromMe: true}, "m2", text("oi")))
	c, _ = hs.arch.GetChat(ctx, shopPN.String())
	if c.Name != shopPN.String() {
		t.Fatalf("own-message chat name=%q, want JID fallback", c.Name)
	}
}

func TestEditAndRevoke(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	hs.watch(t, family, "Família", archive.KindGroup)
	src := types.MessageSource{Chat: family, Sender: momPN, IsGroup: true}
	hs.h.Handle(ctx, message(src, "m1", text("v1")))
	hs.h.Handle(ctx, message(src, "e1", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waCommon.MessageKey{ID: proto.String("m1")},
		EditedMessage: text("v2"),
	}}))
	if got := hs.history(t, family); len(got) != 1 || got[0].Text != "v2" {
		t.Fatalf("after edit=%+v", got)
	}
	hs.h.Handle(ctx, message(src, "r1", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key:  &waCommon.MessageKey{ID: proto.String("m1")},
	}}))
	if got := hs.history(t, family); len(got) != 0 {
		t.Fatalf("after revoke=%+v", got)
	}
}

func TestLoggedOutAndStreamReplacedAreFatal(t *testing.T) {
	hs := newHarness(t)
	hs.h.Handle(context.Background(), &events.LoggedOut{})
	if len(hs.fatal) != 1 || !errors.Is(hs.fatal[0], ErrLoggedOut) {
		t.Fatalf("fatal=%v", hs.fatal)
	}
	if st := hs.h.Tracker.Snapshot().State; st != StateLoggedOut {
		t.Fatalf("state=%s", st)
	}
	hs.h.Handle(context.Background(), &events.StreamReplaced{})
	if len(hs.fatal) != 2 || !errors.Is(hs.fatal[1], ErrStreamReplaced) {
		t.Fatalf("fatal=%v", hs.fatal)
	}
}

func TestConnectedRefreshesCatalogAndStatus(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	hs.h.Handle(ctx, &events.Connected{})

	snap := hs.h.Tracker.Snapshot()
	if snap.State != StateConnected || snap.Account != ownPN.User || snap.PushName != "Leo" {
		t.Fatalf("status=%+v", snap)
	}
	chats, err := hs.arch.ListChats(ctx, archive.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]archive.ChatKind{}
	for _, c := range chats {
		got[c.Name] = c.Kind
	}
	// The LID-only contact is skipped: the catalog is keyed by phone number.
	want := map[string]archive.ChatKind{"Mãe": archive.KindDM, "Família": archive.KindGroup, "Leo (message yourself)": archive.KindSelf}
	if len(got) != len(want) {
		t.Fatalf("catalog=%v want %v", got, want)
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Fatalf("catalog=%v want %v", got, want)
		}
	}
}

func TestHistorySyncCataloguesNamesOnly(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	hs.h.Handle(ctx, &events.HistorySync{Data: &waHistorySync.HistorySync{
		Conversations: []*waHistorySync.Conversation{
			{ID: proto.String(family.String()), Name: proto.String("Família")},
			{ID: proto.String(momLID.String())},
			{ID: proto.String("status@broadcast")},
			{ID: proto.String("not a jid@@")},
		},
	}})
	chats, _ := hs.arch.ListChats(ctx, archive.ListFilter{})
	if len(chats) != 2 {
		t.Fatalf("chats=%+v", chats)
	}
	mom, err := hs.arch.GetChat(ctx, momPN.String())
	if err != nil || mom.Name != "Mãe" {
		t.Fatalf("lid conversation not mapped: %+v %v", mom, err)
	}
	if c, _ := hs.arch.Counts(ctx); c.Messages != 0 {
		t.Fatalf("history sync archived messages: %+v", c)
	}
}

func TestTemporaryBanAndDisconnectUpdateStatus(t *testing.T) {
	hs := newHarness(t)
	hs.h.Handle(context.Background(), &events.Disconnected{})
	if st := hs.h.Tracker.Snapshot().State; st != StateDisconnected {
		t.Fatalf("state=%s", st)
	}
	hs.h.Handle(context.Background(), &events.TemporaryBan{Code: events.TempBanSentToTooManyPeople, Expire: time.Hour})
	snap := hs.h.Tracker.Snapshot()
	if snap.State != StateTemporaryBan || snap.Detail == "" {
		t.Fatalf("status=%+v", snap)
	}
	if len(hs.fatal) != 0 {
		t.Fatal("temporary ban must not stop the daemon")
	}
}
