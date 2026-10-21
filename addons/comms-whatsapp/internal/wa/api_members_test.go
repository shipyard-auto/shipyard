package wa

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// newMembersAPIFixture serves the API over an archive with two tracked
// groups (promoA, promoB) holding member events, one watched message in
// promoA, and a fake WhatsApp behind group info.
func newMembersAPIFixture(t *testing.T) (*apiFixture, *fakeGroups) {
	t.Helper()
	ctx := context.Background()
	arch, err := archive.Open(ctx, filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = arch.Close() })
	for _, c := range []struct {
		jid, name string
		kind      archive.ChatKind
	}{{promoA, "Fisgou Descontos", archive.KindGroup}, {promoB, "Promos Viagem", archive.KindGroup}, {viniJID.String(), "Vini Eduqo", archive.KindDM}} {
		mustDo(t, arch.UpsertChat(ctx, c.jid, c.name, c.kind))
		mustDo(t, arch.Watch(ctx, c.jid, 0))
		if c.kind == archive.KindGroup {
			if _, err := arch.SetMembersTracked(ctx, c.jid, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		chat := promoA
		if i == 2 {
			chat = promoB
		}
		if _, err := arch.InsertGroupEvent(ctx, archive.GroupEvent{ID: fmt.Sprintf("evt_wa_gm_%d", i), ChatJID: chat,
			Timestamp: now.Add(time.Duration(i) * time.Minute), Action: archive.ActionJoin, Member: fmt.Sprintf("hash%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	fg := &fakeGroups{}
	api := &APIServer{
		Archive: arch,
		Token:   func() (string, error) { return testAPIToken, nil },
		Self:    func() types.JID { return ownPN },
		Log:     slog.New(yardlogs.NopHandler()),
		Groups:  &GroupInfoCache{Fetch: fg.fetch, Own: newFakeDir().Own},
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	f := &apiFixture{arch: arch, srv: srv}
	f.add(t, promoA, "m1", now, "oferta")
	return f, fg
}

// The members scope opens membership and nothing else; the read scope
// opens messages and not membership. The sender of offers into its own
// group must not be able to read that group (it would loop).
func TestMembersScopeIsSeparateFromRead(t *testing.T) {
	f, _ := newMembersAPIFixture(t)
	members := "whatsapp.members:" + promoA
	read := "whatsapp.read:" + promoA

	if code := f.call(t, APIMessagesPath, "fisgou", members, MessagesRequest{Chat: promoA}, nil); code != http.StatusForbidden {
		t.Fatalf("members scope read messages: %d", code)
	}
	var msgs MessagesResponse
	if code := f.call(t, APIMessagesPath, "fisgou", members, MessagesRequest{}, &msgs); code != http.StatusOK || len(msgs.Messages) != 0 {
		t.Fatalf("members scope saw messages: %d %+v", code, msgs.Messages)
	}
	for _, path := range []string{APIGroupInfoPath, APIGroupEventsPath} {
		if code := f.call(t, path, "reader", read, map[string]string{"chat": promoA}, nil); code != http.StatusForbidden {
			t.Fatalf("read scope on %s: %d", path, code)
		}
	}
	var evs GroupEventsResponse
	if code := f.call(t, APIGroupEventsPath, "reader", "whatsapp.read", GroupEventsRequest{}, &evs); code != http.StatusOK || len(evs.Events) != 0 {
		t.Fatalf("read-everything scope saw member events: %d %+v", code, evs.Events)
	}
	if code := f.call(t, APIGroupInfoPath, "fisgou", members, GroupInfoRequest{Chat: promoA}, nil); code != http.StatusOK {
		t.Fatalf("members scope group info: %d", code)
	}
	// Another group is not covered by a per-group scope.
	if code := f.call(t, APIGroupInfoPath, "fisgou", members, GroupInfoRequest{Chat: promoB}, nil); code != http.StatusForbidden {
		t.Fatalf("other group: %d", code)
	}
}

func TestGroupEventsCatchUpWithCursor(t *testing.T) {
	f, _ := newMembersAPIFixture(t)
	scope := "whatsapp.members:" + promoA
	var got []string
	after := ""
	for page := 0; ; page++ {
		var resp GroupEventsResponse
		if code := f.call(t, APIGroupEventsPath, "fisgou", scope, GroupEventsRequest{After: after, Limit: 2}, &resp); code != http.StatusOK {
			t.Fatalf("page %d: %d", page, code)
		}
		for _, e := range resp.Events {
			if e.ChatJID != promoA || e.ChatName != "Fisgou Descontos" || e.SchemaVersion != 1 || e.Action != "join" {
				t.Fatalf("event %+v", e)
			}
			got = append(got, e.ID)
		}
		after = resp.NextCursor
		if !resp.HasMore {
			break
		}
	}
	if fmt.Sprint(got) != "[evt_wa_gm_0 evt_wa_gm_1 evt_wa_gm_3 evt_wa_gm_4]" {
		t.Fatalf("got %v", got)
	}
	// Events arriving while the subscriber was away come from the stored cursor.
	mustDo(t, func() error {
		_, err := f.arch.InsertGroupEvent(context.Background(), archive.GroupEvent{ID: "evt_wa_gm_late", ChatJID: promoA,
			Timestamp: time.Now(), Action: archive.ActionLeave, Member: "hash0", Via: ViaLeft})
		return err
	}())
	var resp GroupEventsResponse
	f.call(t, APIGroupEventsPath, "fisgou", scope, GroupEventsRequest{After: after}, &resp)
	if len(resp.Events) != 1 || resp.Events[0].ID != "evt_wa_gm_late" || resp.Events[0].Via == nil || *resp.Events[0].Via != ViaLeft || resp.HasMore {
		t.Fatalf("catch-up %+v", resp)
	}
	// chat narrows; since filters; a DM is not a group.
	f.call(t, APIGroupEventsPath, "all", "whatsapp.members", GroupEventsRequest{Chat: promoB}, &resp)
	if len(resp.Events) != 1 || resp.Events[0].ID != "evt_wa_gm_2" {
		t.Fatalf("chat filter %+v", resp.Events)
	}
	f.call(t, APIGroupEventsPath, "all", "whatsapp.members", GroupEventsRequest{Since: time.Now().Add(150 * time.Second).Format(time.RFC3339)}, &resp)
	if len(resp.Events) != 2 {
		t.Fatalf("since filter %+v", resp.Events)
	}
	if code := f.call(t, APIGroupEventsPath, "all", "whatsapp.members", GroupEventsRequest{Chat: viniJID.String()}, nil); code != http.StatusBadRequest {
		t.Fatalf("dm: %d", code)
	}
}

func TestGroupInfoAPICachesAndThrottles(t *testing.T) {
	f, fg := newMembersAPIFixture(t)
	scope := "whatsapp.members"
	var info GroupInfo
	if code := f.call(t, APIGroupInfoPath, "fisgou", scope, GroupInfoRequest{Chat: "Fisgou Descontos"}, &info); code != http.StatusOK ||
		info.Participants != 4 || info.Admins != 2 || !info.SelfIsAdmin || info.Cached {
		t.Fatalf("%d %+v", code, info)
	}
	f.call(t, APIGroupInfoPath, "fisgou", scope, GroupInfoRequest{Chat: promoA}, &info)
	if !info.Cached || fg.calls != 1 {
		t.Fatalf("second call fetched again: calls=%d", fg.calls)
	}
	// A failed fetch cannot be retried within the minute.
	fg.err = fmt.Errorf("timeout")
	if code := f.call(t, APIGroupInfoPath, "fisgou", scope, GroupInfoRequest{Chat: promoB}, nil); code != http.StatusBadGateway {
		t.Fatalf("fetch failure: %d", code)
	}
	var b apiErrorBody
	if code := f.call(t, APIGroupInfoPath, "fisgou", scope, GroupInfoRequest{Chat: promoB}, &b); code != http.StatusTooManyRequests ||
		b.Error.Code != "rate_limited" || b.Error.RetryAfterSeconds < 1 || fg.calls != 2 {
		t.Fatalf("%d %+v calls=%d", code, b, fg.calls)
	}
	if code := f.call(t, APIGroupInfoPath, "fisgou", scope, GroupInfoRequest{Chat: viniJID.String()}, nil); code != http.StatusBadRequest {
		t.Fatalf("dm: %d", code)
	}
}
