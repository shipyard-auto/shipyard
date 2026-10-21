package archive

import (
	"context"
	"errors"
	"testing"
	"time"
)

func gev(id, chat string, ts time.Time, action, member string) GroupEvent {
	return GroupEvent{ID: id, ChatJID: chat, Timestamp: ts, Action: action, Member: member}
}

func TestGroupEventsOnlyForTrackedGroupsAndDeduped(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, "g1@g.us", "Ofertas", KindGroup))
	must(t, s.UpsertChat(ctx, "dm@s.whatsapp.net", "Vini", KindDM))

	if ok, err := s.InsertGroupEvent(ctx, gev("e1", "g1@g.us", t0, ActionJoin, "h1")); err != nil || ok {
		t.Fatalf("untracked group stored an event: %v %v", ok, err)
	}
	if _, err := s.SetMembersTracked(ctx, "dm@s.whatsapp.net", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tracking a DM: %v", err)
	}
	if _, err := s.SetMembersTracked(ctx, "g1@g.us", true); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.MembersTracked(ctx, "g1@g.us"); !on {
		t.Fatal("not tracked")
	}
	for i, id := range []string{"e1", "e1", "e2"} {
		ok, err := s.InsertGroupEvent(ctx, gev(id, "g1@g.us", t0, ActionJoin, "h1"))
		if err != nil || ok == (i == 1) {
			t.Fatalf("insert %d (%s): ok=%v err=%v", i, id, ok, err)
		}
	}
	groups, err := s.ListTrackedGroups(ctx)
	if err != nil || len(groups) != 1 || groups[0].Events != 2 || groups[0].Name != "Ofertas" {
		t.Fatalf("tracked=%+v err=%v", groups, err)
	}
	n, err := s.SetMembersTracked(ctx, "g1@g.us", false)
	if err != nil || n != 2 {
		t.Fatalf("untrack deleted %d: %v", n, err)
	}
	if ok, _ := s.InsertGroupEvent(ctx, gev("e3", "g1@g.us", t0, ActionLeave, "h1")); ok {
		t.Fatal("stored after untrack")
	}
}

func TestGroupEventsCursorFiltersAndPrune(t *testing.T) {
	s, now := openTest(t)
	ctx := context.Background()
	for _, g := range []string{"a@g.us", "b@g.us"} {
		must(t, s.UpsertChat(ctx, g, g, KindGroup))
		if _, err := s.SetMembersTracked(ctx, g, true); err != nil {
			t.Fatal(err)
		}
	}
	for i, e := range []GroupEvent{
		gev("1", "a@g.us", t0.Add(-100*24*time.Hour), ActionJoin, "h1"),
		gev("2", "b@g.us", t0.Add(-time.Hour), ActionJoin, "h2"),
		{ID: "3", ChatJID: "a@g.us", Timestamp: t0, Action: ActionLeave, Member: "h1", Actor: "h9", Via: "removed"},
	} {
		if ok, err := s.InsertGroupEvent(ctx, e); !ok || err != nil {
			t.Fatalf("insert %d: %v %v", i, ok, err)
		}
	}
	all, err := s.GroupEvents(ctx, GroupEventQuery{Limit: 10})
	if err != nil || len(all) != 3 || all[0].ID != "1" || all[2].Actor != "h9" || all[2].Via != "removed" || all[1].ChatName != "b@g.us" {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	page, _ := s.GroupEvents(ctx, GroupEventQuery{After: all[0].Seq, Limit: 1})
	if len(page) != 1 || page[0].ID != "2" {
		t.Fatalf("after cursor: %+v", page)
	}
	onlyA, _ := s.GroupEvents(ctx, GroupEventQuery{Chat: "a@g.us", Since: t0.Add(-2 * time.Hour), Limit: 10})
	if len(onlyA) != 1 || onlyA[0].ID != "3" {
		t.Fatalf("chat+since: %+v", onlyA)
	}
	*now = t0
	n, err := s.PruneGroupEvents(ctx, 90*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	// The cursor stays valid after pruning: seq is never reused.
	if ok, _ := s.InsertGroupEvent(ctx, gev("4", "a@g.us", t0, ActionPromote, "h1")); !ok {
		t.Fatal("insert after prune")
	}
	rest, _ := s.GroupEvents(ctx, GroupEventQuery{After: all[2].Seq, Limit: 10})
	if len(rest) != 1 || rest[0].ID != "4" {
		t.Fatalf("after prune: %+v", rest)
	}
}
