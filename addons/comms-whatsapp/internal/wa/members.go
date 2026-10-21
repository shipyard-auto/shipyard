package wa

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// Membership events: who joined, left, was promoted or demoted in the
// groups whose members are tracked ('members track'). Members are never
// identified by phone number or JID — not in the archive, the events or
// the API — only by a keyed hash that is stable on this installation, so
// "did the person who left join earlier?" can be answered without
// knowing who they are.

// MembersScopeName lets a client see a group's membership: group info,
// member events through the API and the whatsapp.group.members topic.
// "whatsapp.members:<group jid>", a glob, or every group. It grants no
// access to messages, and whatsapp.read grants none to membership.
const MembersScopeName = "whatsapp.members"

// MembersScope is the scope a client needs for one group's membership.
func MembersScope(chatJID string) string { return MembersScopeName + ":" + chatJID }

// MembersEventTopic is the topic membership changes are published under.
const MembersEventTopic = "whatsapp.group.members"

// DefaultMembersTTL is how long membership events are kept.
const DefaultMembersTTL = 90 * 24 * time.Hour

// How a member joined or left, when WhatsApp says.
const (
	ViaInviteLink = "invite_link"
	ViaAdded      = "added"
	ViaRemoved    = "removed"
	ViaLeft       = "left"
)

// memberHashBytes is how much of the HMAC is kept: 128 bits, 32 hex chars.
const memberHashBytes = 16

// MemberHasher pseudonymizes member identities with HMAC-SHA256 under a
// per-installation secret. Losing the secret (logout --purge, a new
// machine) starts a new, unlinkable series of hashes.
type MemberHasher struct{ key []byte }

// NewMemberHasher returns a hasher for key.
func NewMemberHasher(key []byte) MemberHasher { return MemberHasher{key: key} }

// Hash returns the member id of a canonical JID string.
func (h MemberHasher) Hash(jid string) string {
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte(jid))
	return hex.EncodeToString(m.Sum(nil)[:memberHashBytes])
}

// EnsureMembersKey returns the installation's member-hash secret,
// creating it (0600) on first use. It must outlive restarts and upgrades:
// a new secret changes every hash.
func EnsureMembersKey(path string) ([]byte, error) {
	if t, err := ReadAPIToken(path); err != nil || t != "" {
		if err != nil {
			return nil, fmt.Errorf("members: read key: %w", err)
		}
		key, err := hex.DecodeString(t)
		if err != nil || len(key) < 32 {
			return nil, fmt.Errorf("members: key file %s is corrupt; restore it from a backup (a new key changes every member hash)", path)
		}
		return key, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("members: key: %w", err)
	}
	if err := writeFileAtomic(path, []byte(hex.EncodeToString(key)+"\n")); err != nil {
		return nil, err
	}
	return key, nil
}

// MemberEvent is the whatsapp.group.members envelope, schema v1: one
// membership change of one member. Actor and Via are null when WhatsApp
// does not say.
type MemberEvent struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	Timestamp     time.Time `json:"ts"`
	ChatJID       string    `json:"chat_jid"`
	ChatName      string    `json:"chat_name"`
	Action        string    `json:"action"` // join|leave|promote|demote
	Member        string    `json:"member"`
	Actor         *string   `json:"actor"`
	Via           *string   `json:"via"`
}

// memberEvent renders an archived row as the envelope, so the webhook and
// the catch-up API carry one shape.
func memberEvent(e archive.GroupEvent) MemberEvent {
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	return MemberEvent{
		SchemaVersion: 1, ID: e.ID, Timestamp: e.Timestamp.UTC(), ChatJID: e.ChatJID, ChatName: e.ChatName,
		Action: e.Action, Member: e.Member, Actor: opt(e.Actor), Via: opt(e.Via),
	}
}

// MemberEvents records and publishes membership changes of tracked groups.
type MemberEvents struct {
	Archive *archive.Store
	Dir     Directory
	Hasher  MemberHasher
	// Publish sends one event to fairway; nil disables publishing (the
	// archive still records it).
	Publish func(MemberEvent) bool
	Log     *slog.Logger
}

// OnGroupInfo handles a whatsmeow group change. Only participant changes
// of tracked groups produce events; everything else is ignored.
func (m *MemberEvents) OnGroupInfo(ctx context.Context, e *events.GroupInfo) {
	if len(e.Join)+len(e.Leave)+len(e.Promote)+len(e.Demote) == 0 {
		return
	}
	chat := e.JID.ToNonAD().String()
	tracked, err := m.Archive.MembersTracked(ctx, chat)
	if err != nil || !tracked {
		return
	}
	chatName := chat
	if c, err := m.Archive.GetChat(ctx, chat); err == nil {
		chatName = c.Name
	}
	actor := ""
	if e.Sender != nil && !e.Sender.IsEmpty() {
		actor = m.memberID(ctx, *e.Sender)
	}
	ts := e.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	invite := e.JoinReason == "invite" || e.Notify == "invite"
	for _, set := range []struct {
		action string
		jids   []types.JID
	}{{archive.ActionJoin, e.Join}, {archive.ActionLeave, e.Leave}, {archive.ActionPromote, e.Promote}, {archive.ActionDemote, e.Demote}} {
		for _, j := range set.jids {
			member := m.memberID(ctx, j)
			ev := archive.GroupEvent{
				ChatJID: chat, ChatName: chatName, Timestamp: ts.UTC().Truncate(time.Second),
				Action: set.action, Member: member, Actor: actor, Via: memberVia(set.action, member, actor, invite),
			}
			ev.ID = memberEventID(chat, e.ParticipantVersionID, ev)
			written, err := m.Archive.InsertGroupEvent(ctx, ev)
			if err != nil {
				m.Log.Error(yardlogs.EventWAArchiveError, slog.String(yardlogs.KeyMessage, "member event not stored"),
					slog.String(yardlogs.KeyChatJID, chat), slog.String(yardlogs.KeyError, err.Error()))
				continue
			}
			if !written {
				continue // untracked meanwhile, or a redelivery already stored
			}
			m.Log.Info(yardlogs.EventWAGroupMember, slog.String(yardlogs.KeyMessage, "group member "+set.action),
				slog.String(yardlogs.KeyChatJID, chat))
			if m.Publish != nil {
				m.Publish(memberEvent(ev))
			}
		}
	}
}

// memberID hashes the canonical identity of a user. The LID is preferred:
// it is what LID-addressed groups report, and it does not change when
// WhatsApp starts or stops sharing the phone number with us. A phone
// number whose LID is not known yet is hashed as it is.
func (m *MemberEvents) memberID(ctx context.Context, j types.JID) string {
	j = j.ToNonAD()
	if j.Server == types.DefaultUserServer {
		if lid, ok := m.Dir.LIDForPN(ctx, j); ok {
			j = lid.ToNonAD()
		}
	}
	return m.Hasher.Hash(j.String())
}

// memberVia infers how a member arrived or left. A change made by the
// member themself is a link join or leaving; one made by someone else is
// an add or a removal.
func memberVia(action, member, actor string, invite bool) string {
	switch action {
	case archive.ActionJoin:
		switch {
		case invite:
			return ViaInviteLink
		case actor != "" && actor != member:
			return ViaAdded
		}
	case archive.ActionLeave:
		switch {
		case actor != "" && actor != member:
			return ViaRemoved
		case actor == member:
			return ViaLeft
		}
	}
	return ""
}

// memberEventID derives a stable id: WhatsApp may deliver one change
// twice (reconnects), and both copies must dedupe to one event.
func memberEventID(chat, version string, e archive.GroupEvent) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{chat, version, e.Action, e.Member,
		fmt.Sprint(e.Timestamp.Unix())}, "\x00")))
	return "evt_wa_gm_" + hex.EncodeToString(sum[:12])
}
