package wa

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type fakeGroups struct {
	calls int
	err   error
}

func (f *fakeGroups) fetch(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &types.GroupInfo{JID: jid, GroupName: types.GroupName{Name: "Fisgou"}, Participants: []types.GroupParticipant{
		{JID: ownLID, IsAdmin: true},
		{JID: momLID, IsSuperAdmin: true},
		{JID: shopLID},
		{JID: types.NewJID("99000000000009", types.HiddenUserServer)},
	}}, nil
}

func TestGroupInfoCountsOnlyAndSelfAdmin(t *testing.T) {
	f := &fakeGroups{}
	c := &GroupInfoCache{Fetch: f.fetch, Own: newFakeDir().Own}
	info, err := c.Get(context.Background(), family, false)
	if err != nil || info.Participants != 4 || info.Admins != 2 || !info.SelfIsAdmin || info.Name != "Fisgou" ||
		info.JID != family.String() || info.Cached {
		t.Fatalf("info=%+v err=%v", info, err)
	}
}

func TestGroupInfoCacheAndPace(t *testing.T) {
	now := ts
	f := &fakeGroups{}
	c := &GroupInfoCache{Fetch: f.fetch, Own: newFakeDir().Own, Now: func() time.Time { return now }}
	ctx := context.Background()
	get := func(refresh bool) GroupInfo {
		t.Helper()
		info, err := c.Get(ctx, family, refresh)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	get(false)
	now = now.Add(9 * time.Minute)
	if i := get(false); !i.Cached || f.calls != 1 {
		t.Fatalf("within TTL: cached=%v calls=%d", i.Cached, f.calls)
	}
	now = now.Add(2 * time.Minute) // 11 min: expired
	if i := get(false); i.Cached || f.calls != 2 {
		t.Fatalf("after TTL: cached=%v calls=%d", i.Cached, f.calls)
	}
	now = now.Add(30 * time.Second)
	if i := get(true); !i.Cached || f.calls != 2 {
		t.Fatalf("refresh within a minute must not fetch: calls=%d", f.calls)
	}
	now = now.Add(31 * time.Second)
	if i := get(true); i.Cached || f.calls != 3 {
		t.Fatalf("refresh after a minute: calls=%d", f.calls)
	}
	// Another group has its own pace.
	if _, err := c.Get(ctx, types.NewJID("120363000000000077", types.GroupServer), false); err != nil || f.calls != 4 {
		t.Fatalf("other group: %v calls=%d", err, f.calls)
	}
}

func TestGroupInfoFailureIsPacedToo(t *testing.T) {
	now := ts
	f := &fakeGroups{err: whatsmeow.ErrNotInGroup}
	c := &GroupInfoCache{Fetch: f.fetch, Now: func() time.Time { return now }}
	ctx := context.Background()
	if _, err := c.Get(ctx, family, false); !errors.Is(err, ErrNotInGroup) {
		t.Fatalf("first: %v", err)
	}
	now = now.Add(20 * time.Second)
	_, err := c.Get(ctx, family, true)
	var th *GroupInfoThrottled
	if !errors.As(err, &th) || th.RetryAfter != 40*time.Second || f.calls != 1 {
		t.Fatalf("second: %v calls=%d", err, f.calls)
	}
	now = now.Add(time.Minute)
	f.err = nil
	if _, err := c.Get(ctx, family, false); err != nil || f.calls != 2 {
		t.Fatalf("after pace: %v calls=%d", err, f.calls)
	}
}
