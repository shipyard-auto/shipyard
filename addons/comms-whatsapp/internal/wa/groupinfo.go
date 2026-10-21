package wa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// Group info asks WhatsApp's servers for a group's metadata. A client
// polling it would look like a bot, so answers are cached and each group
// is fetched at most once per GroupInfoMinInterval, whoever asks.

// Group info pacing.
const (
	GroupInfoTTL         = 10 * time.Minute
	GroupInfoMinInterval = time.Minute
	groupInfoTimeout     = 20 * time.Second
)

// GroupInfo is a group's member counts. The member list itself is never
// exposed: counts are all analytics need.
type GroupInfo struct {
	JID          string    `json:"jid"`
	Name         string    `json:"name"`
	Participants int       `json:"participants"`
	Admins       int       `json:"admins"` // admins and the owner (super admin)
	SelfIsAdmin  bool      `json:"self_is_admin"`
	FetchedAt    time.Time `json:"fetched_at"`
	// Cached is true when the answer came from the cache (FetchedAt says
	// how old it is).
	Cached bool `json:"cached"`
}

// Group info failures.
var (
	ErrNotInGroup = errors.New("this account is not a member of the group")
	// ErrGroupInfoThrottled is wrapped in *GroupInfoThrottled.
	ErrGroupInfoThrottled = errors.New("group info fetched too recently")
)

// GroupInfoThrottled is returned when a fetch would exceed the per-group
// pace and there is no cached answer to fall back on (the last fetch
// failed).
type GroupInfoThrottled struct{ RetryAfter time.Duration }

func (e *GroupInfoThrottled) Error() string {
	return fmt.Sprintf("%v; try again in %s", ErrGroupInfoThrottled, e.RetryAfter.Round(time.Second))
}
func (e *GroupInfoThrottled) Unwrap() error { return ErrGroupInfoThrottled }

// GroupInfoCache serves GroupInfo with caching and per-group pacing.
type GroupInfoCache struct {
	Fetch func(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
	// Own returns this account's phone JID and LID, for self_is_admin.
	Own func() (pn, lid types.JID)
	Now func() time.Time
	Log *slog.Logger

	mu      sync.Mutex
	entries map[string]*groupInfoEntry
}

type groupInfoEntry struct {
	mu          sync.Mutex // held across a fetch: concurrent askers wait for it
	info        *GroupInfo
	lastAttempt time.Time
}

// Get returns jid's info, from the cache when younger than GroupInfoTTL.
// refresh asks for a fresh fetch, still subject to GroupInfoMinInterval:
// within it the cached answer is returned.
func (c *GroupInfoCache) Get(ctx context.Context, jid types.JID, refresh bool) (GroupInfo, error) {
	key := jid.ToNonAD().String()
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*groupInfoEntry{}
	}
	e, ok := c.entries[key]
	if !ok {
		e = &groupInfoEntry{}
		c.entries[key] = e
	}
	c.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	now := c.now()
	paced := !e.lastAttempt.IsZero() && now.Sub(e.lastAttempt) < GroupInfoMinInterval
	if e.info != nil && (paced || (!refresh && now.Sub(e.info.FetchedAt) < GroupInfoTTL)) {
		out := *e.info
		out.Cached = true
		return out, nil
	}
	if paced {
		return GroupInfo{}, &GroupInfoThrottled{RetryAfter: GroupInfoMinInterval - now.Sub(e.lastAttempt)}
	}
	e.lastAttempt = now
	fctx, cancel := context.WithTimeout(ctx, groupInfoTimeout)
	defer cancel()
	raw, err := c.Fetch(fctx, jid)
	if err != nil {
		if errors.Is(err, whatsmeow.ErrNotInGroup) || errors.Is(err, whatsmeow.ErrGroupNotFound) {
			err = ErrNotInGroup
		}
		e.info = nil
		return GroupInfo{}, err
	}
	info := summarizeGroup(raw, c.Own, now)
	e.info = &info
	if c.Log != nil {
		c.Log.Info(yardlogs.EventWAGroupInfo, slog.String(yardlogs.KeyMessage, "group info fetched"),
			slog.String(yardlogs.KeyChatJID, key), slog.Int(yardlogs.KeyCount, info.Participants))
	}
	return info, nil
}

func (c *GroupInfoCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// summarizeGroup reduces whatsmeow's group info to counts.
func summarizeGroup(g *types.GroupInfo, own func() (types.JID, types.JID), now time.Time) GroupInfo {
	out := GroupInfo{JID: g.JID.ToNonAD().String(), Name: g.Name, Participants: max(g.ParticipantCount, len(g.Participants)), FetchedAt: now.UTC()}
	var pn, lid types.JID
	if own != nil {
		pn, lid = own()
	}
	isSelf := func(j types.JID) bool {
		return !j.IsEmpty() && ((!pn.IsEmpty() && j.User == pn.User && j.Server == pn.Server) ||
			(!lid.IsEmpty() && j.User == lid.User && j.Server == lid.Server))
	}
	for _, p := range g.Participants {
		if !p.IsAdmin && !p.IsSuperAdmin {
			continue
		}
		out.Admins++
		if isSelf(p.JID) || isSelf(p.PhoneNumber) || isSelf(p.LID) {
			out.SelfIsAdmin = true
		}
	}
	return out
}
