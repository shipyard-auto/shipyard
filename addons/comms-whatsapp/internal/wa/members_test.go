package wa

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

var testMembersKey = []byte("0123456789abcdef0123456789abcdef")

// membersHarness tracks family's members and collects published events.
func membersHarness(t *testing.T) (*harness, *[]MemberEvent) {
	t.Helper()
	hs := newHarness(t)
	var published []MemberEvent
	hs.h.Members = &MemberEvents{Archive: hs.arch, Dir: hs.h.Dir, Hasher: NewMemberHasher(testMembersKey), Log: hs.h.Log,
		Publish: func(ev MemberEvent) bool { published = append(published, ev); return true }}
	ctx := context.Background()
	mustDo(t, hs.arch.UpsertChat(ctx, family.String(), "Família", archive.KindGroup))
	if _, err := hs.arch.SetMembersTracked(ctx, family.String(), true); err != nil {
		t.Fatal(err)
	}
	return hs, &published
}

func TestMemberEventsArePublishedWithHashesOnly(t *testing.T) {
	hs, published := membersHarness(t)
	ctx := context.Background()
	admin := momLID
	// Mom joins by link (reported by phone number), then an admin removes
	// her (reported by LID), then shop is added and promoted.
	hs.h.Handle(ctx, &events.GroupInfo{JID: family, Timestamp: ts, JoinReason: "invite", Join: []types.JID{momPN}, Sender: &momPN, ParticipantVersionID: "v1"})
	hs.h.Handle(ctx, &events.GroupInfo{JID: family, Timestamp: ts.Add(1), Leave: []types.JID{momLID}, Sender: &ownPN, ParticipantVersionID: "v2"})
	hs.h.Handle(ctx, &events.GroupInfo{JID: family, Timestamp: ts.Add(2e9), Join: []types.JID{shopPN}, Sender: &admin, ParticipantVersionID: "v3"})
	hs.h.Handle(ctx, &events.GroupInfo{JID: family, Timestamp: ts.Add(3e9), Promote: []types.JID{shopPN}, ParticipantVersionID: "v4"})
	// Leaving on one's own.
	hs.h.Handle(ctx, &events.GroupInfo{JID: family, Timestamp: ts.Add(4e9), Leave: []types.JID{shopPN}, Sender: &shopPN, ParticipantVersionID: "v5"})

	got := *published
	if len(got) != 5 {
		t.Fatalf("published %d events: %+v", len(got), got)
	}
	str := func(p *string) string {
		if p == nil {
			return "null"
		}
		return *p
	}
	want := []struct{ action, via string }{{"join", ViaInviteLink}, {"leave", ViaRemoved}, {"join", ViaAdded}, {"promote", "null"}, {"leave", ViaLeft}}
	for i, w := range want {
		if got[i].Action != w.action || str(got[i].Via) != w.via || got[i].ChatName != "Família" || got[i].SchemaVersion != 1 {
			t.Fatalf("event %d = %+v (via %s), want %v", i, got[i], str(got[i].Via), w)
		}
	}
	// The same person keeps one hash whether reported by phone or LID.
	if got[0].Member != got[1].Member || len(got[0].Member) != 32 {
		t.Fatalf("mom hashed as %q and %q", got[0].Member, got[1].Member)
	}
	if got[3].Actor != nil {
		t.Fatalf("unknown actor should be null: %v", *got[3].Actor)
	}
	// Nothing identifying leaves the daemon: not in events, not in the archive.
	raw, _ := json.Marshal(got)
	stored, err := hs.arch.GroupEvents(ctx, archive.GroupEventQuery{Limit: 100})
	if err != nil || len(stored) != 5 {
		t.Fatalf("stored %d: %v", len(stored), err)
	}
	rawStored, _ := json.Marshal(stored)
	for _, j := range []types.JID{momPN, momLID, shopPN, ownPN, ownLID} {
		if strings.Contains(string(raw), j.User) || strings.Contains(string(rawStored), j.User) {
			t.Fatalf("%s appears in clear:\n%s\n%s", j.User, raw, rawStored)
		}
	}
	if !strings.Contains(string(raw), `"actor":null`) || !strings.Contains(string(raw), `"via":null`) {
		t.Fatalf("null fields missing: %s", raw)
	}
}

func TestMemberEventsIgnoreUntrackedGroupsAndRedeliveries(t *testing.T) {
	hs, published := membersHarness(t)
	ctx := context.Background()
	other := types.NewJID("120363000000000099", types.GroupServer)
	mustDo(t, hs.arch.UpsertChat(ctx, other.String(), "Outro", archive.KindGroup))
	hs.h.Handle(ctx, &events.GroupInfo{JID: other, Timestamp: ts, Join: []types.JID{momPN}})
	ev := &events.GroupInfo{JID: family, Timestamp: ts, Join: []types.JID{momPN, shopPN}, ParticipantVersionID: "v1"}
	hs.h.Handle(ctx, ev)
	hs.h.Handle(ctx, ev) // WhatsApp redelivers after a reconnect
	// A rename without participant changes is no member event.
	hs.h.Handle(ctx, &events.GroupInfo{JID: family, Timestamp: ts, Name: &types.GroupName{Name: "Família 2"}})
	if len(*published) != 2 {
		t.Fatalf("published %+v", *published)
	}
	if c, _ := hs.arch.GetChat(ctx, family.String()); c.Name != "Família 2" {
		t.Fatalf("rename not applied: %q", c.Name)
	}
}

func TestMemberHashIsStablePerKeyAndKeyPersists(t *testing.T) {
	a, b := NewMemberHasher(testMembersKey), NewMemberHasher([]byte("another-installation-secret-0000"))
	if a.Hash(momLID.String()) != a.Hash(momLID.String()) || a.Hash(momLID.String()) == b.Hash(momLID.String()) {
		t.Fatal("hash must be stable per key and differ across keys")
	}
	path := filepath.Join(t.TempDir(), "members.key")
	k1, err := EnsureMembersKey(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := EnsureMembersKey(path)
	if err != nil || string(k1) != string(k2) || len(k1) != 32 {
		t.Fatalf("key changed across calls: %v", err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", st.Mode())
	}
	mustDo(t, os.WriteFile(path, []byte("garbage\n"), 0o600))
	if _, err := EnsureMembersKey(path); err == nil {
		t.Fatal("a corrupt key must not be silently replaced")
	}
}
