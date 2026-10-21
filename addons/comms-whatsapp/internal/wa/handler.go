package wa

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// Directory answers identity and naming questions from the linked
// device's own store. It is an interface so the handler can be tested
// without a WhatsApp connection.
type Directory interface {
	// Own returns this account's phone-number JID and LID (either may be
	// empty before pairing completes).
	Own() (pn, lid types.JID)
	OwnPushName() string
	// PNForLID maps a hidden-user LID to its phone-number JID.
	PNForLID(ctx context.Context, lid types.JID) (types.JID, bool)
	// LIDForPN is the reverse mapping.
	LIDForPN(ctx context.Context, pn types.JID) (types.JID, bool)
	// ContactName is the best local name for a user: saved contact name,
	// then push name, then business name. Empty when unknown.
	ContactName(ctx context.Context, jid types.JID) string
	// GroupName fetches the subject of a group (may hit the network).
	GroupName(ctx context.Context, jid types.JID) string
	// Contacts lists every user in the device's contact store with a name.
	// One person can appear twice — under their phone JID and their LID —
	// often with the address-book name on one entry only.
	Contacts(ctx context.Context) (map[types.JID]ContactName, error)
	// Groups lists joined groups with their subjects (network call).
	Groups(ctx context.Context) (map[types.JID]string, error)
}

// Handler turns whatsmeow events into archive writes and status changes.
type Handler struct {
	Archive *archive.Store
	Dir     Directory
	Tracker *Tracker
	Log     *slog.Logger
	// OnFatal is called (once per occurrence) when the session can no
	// longer continue: logged out by the phone, or replaced by another
	// client using the same keys.
	OnFatal func(error)
	// Async runs background work (catalog refresh). Defaults to `go f()`;
	// tests make it synchronous.
	Async func(func())
	// Routes and Deliver forward received messages of routed chats to
	// fairway. Both nil disables delivery (pairing, tests).
	Routes  func() Routes
	Deliver func(path string, ev Event) bool
	// Publishes returns the chats selected for fairway's events API, and
	// Publish publishes one message. Either nil disables publishing.
	Publishes func() PublishSelection
	Publish   func(ev Event) bool
	// Media returns the attachment download policy and Download queues one
	// (false = queue full). Either nil disables downloads.
	Media    func() MediaConfig
	Download func(MediaJob) bool
	// Members records membership changes of tracked groups; nil ignores
	// them.
	Members *MemberEvents
}

// Fatal errors passed to OnFatal.
var (
	ErrLoggedOut      = fmt.Errorf("the phone removed this linked device; run 'shipyard-comms-whatsapp pair' again")
	ErrStreamReplaced = fmt.Errorf("another client connected with this session; only one daemon may run per session")
)

// Handle is registered with whatsmeow's AddEventHandler. It must not block
// for long: whatsmeow delivers events sequentially.
func (h *Handler) Handle(ctx context.Context, evt any) {
	switch e := evt.(type) {
	case *events.Message:
		h.onMessage(ctx, e)
	case *events.HistorySync:
		h.onHistorySync(ctx, e)
	case *events.GroupInfo:
		h.onGroupInfo(ctx, e)
	case *events.Connected:
		h.Tracker.SetState(StateConnected, "")
		pn, _ := h.Dir.Own()
		h.Tracker.SetAccount(pn.User, h.Dir.OwnPushName())
		h.Log.Info(yardlogs.EventWAConnected, slog.String(yardlogs.KeyMessage, "connected to WhatsApp"))
		h.async(func() { h.RefreshCatalog(ctx) })
	case *events.Disconnected:
		h.Tracker.SetState(StateDisconnected, "")
		h.Log.Warn(yardlogs.EventWADisconnected, slog.String(yardlogs.KeyMessage, "disconnected from WhatsApp; reconnecting"))
	case *events.KeepAliveTimeout:
		h.Tracker.SetState(StateDisconnected, fmt.Sprintf("keepalive timeout (%d errors)", e.ErrorCount))
	case *events.KeepAliveRestored:
		h.Tracker.SetState(StateConnected, "")
	case *events.LoggedOut:
		h.Tracker.SetState(StateLoggedOut, e.Reason.String())
		h.Log.Error(yardlogs.EventWALoggedOut, slog.String(yardlogs.KeyMessage, "logged out by the phone"),
			slog.String(yardlogs.KeyError, e.Reason.String()))
		h.fatal(ErrLoggedOut)
	case *events.StreamReplaced:
		h.Tracker.SetState(StateStreamReplace, "")
		h.Log.Error(yardlogs.EventWAStreamReplaced, slog.String(yardlogs.KeyMessage, "session replaced by another client"))
		h.fatal(ErrStreamReplaced)
	case *events.TemporaryBan:
		h.Tracker.SetState(StateTemporaryBan, e.String())
		h.Log.Error(yardlogs.EventWATemporaryBan, slog.String(yardlogs.KeyMessage, "account temporarily banned"),
			slog.String(yardlogs.KeyError, e.String()))
	}
}

func (h *Handler) async(f func()) {
	if h.Async != nil {
		h.Async(f)
		return
	}
	go f()
}

func (h *Handler) fatal(err error) {
	if h.OnFatal != nil {
		h.OnFatal(err)
	}
}

func (h *Handler) onMessage(ctx context.Context, e *events.Message) {
	h.Tracker.MessageSeen(e.Info.Timestamp)
	content := ExtractContent(e.Message)
	if content.Action == ActionIgnore {
		return
	}
	chat, kind, ok := h.normalizeChat(ctx, e.Info.MessageSource)
	if !ok {
		return
	}
	chatJID := chat.String()
	if err := h.ensureChat(ctx, chat, kind, e.Info); err != nil {
		h.archiveError(chatJID, err)
		return
	}
	// Remember that a conversation exists, watched or not: sending is
	// only allowed into chats with prior activity.
	if content.Action == ActionStore {
		if err := h.Archive.TouchActivity(ctx, chatJID, e.Info.Timestamp); err != nil {
			h.archiveError(chatJID, err)
		}
	}

	var err error
	switch content.Action {
	case ActionStore:
		sender := h.normalizeSender(ctx, e.Info.MessageSource)
		name := ""
		if !e.Info.IsFromMe {
			if name = h.Dir.ContactName(ctx, sender); name == "" {
				name = e.Info.PushName
			}
		}
		msg := archive.Message{
			ChatJID:    chatJID,
			ID:         e.Info.ID,
			SenderJID:  sender.String(),
			SenderName: name,
			FromMe:     e.Info.IsFromMe,
			Timestamp:  e.Info.Timestamp,
			Kind:       content.Kind,
			Text:       content.Text,
			ReplyTo:    content.ReplyTo,
		}
		if content.Media != nil {
			info := content.Media.Info
			info.Status, info.Reason = archive.MediaSkipped, "downloads are disabled"
			if h.Media != nil && h.Download != nil {
				info.Status, info.Reason = mediaDecision(h.Media(), content.Kind, content.Media)
			}
			msg.Media = &info
		}
		var written bool
		written, err = h.Archive.InsertMessage(ctx, msg)
		if err == nil {
			media := msg.Media
			if media != nil && !written {
				// Unwatched chat: nothing is archived, so nothing is fetched.
				media.Status, media.Reason = archive.MediaSkipped, "chat is not watched"
			}
			if written && media != nil && media.Status == archive.MediaPending {
				if h.queueDownload(ctx, e, chat, kind, sender, name, content) {
					return // routed once the download ends
				}
				media.Status, media.Reason = archive.MediaFailed, "download queue full"
				err = h.Archive.SetMediaStatus(ctx, chatJID, e.Info.ID, media.Status, media.Reason)
			}
			h.route(ctx, e, chat, kind, sender, name, content, media)
		}
	case ActionEdit:
		err = h.Archive.EditMessage(ctx, chatJID, content.TargetID, content.Kind, content.Text)
	case ActionRevoke:
		err = h.Archive.DeleteMessage(ctx, chatJID, content.TargetID)
	}
	if err != nil {
		h.archiveError(chatJID, err)
	}
}

// onGroupInfo keeps a renamed group's catalog name current and hands
// participant changes to the member tracker.
func (h *Handler) onGroupInfo(ctx context.Context, e *events.GroupInfo) {
	chat := e.JID.ToNonAD().String()
	if e.Name != nil && e.Name.Name != "" {
		if known, err := h.Archive.Known(ctx, chat); err == nil && known {
			if err := h.Archive.UpsertChat(ctx, chat, e.Name.Name, archive.KindGroup); err != nil {
				h.archiveError(chat, err)
			}
		}
	}
	if h.Members != nil {
		h.Members.OnGroupInfo(ctx, e)
	}
}

// queueDownload schedules the attachment of an archived message and
// routes the message when the download ends, so an agent receives the
// file along with the event. False means the queue was full.
func (h *Handler) queueDownload(ctx context.Context, e *events.Message, chat types.JID, kind archive.ChatKind,
	sender types.JID, senderName string, content Content) bool {
	chatJID := chat.String()
	return h.Download(MediaJob{
		ChatJID: chatJID, MsgID: e.Info.ID, Ref: content.Media,
		Done: func() {
			media, err := h.Archive.GetMedia(ctx, chatJID, e.Info.ID)
			if err != nil {
				media = content.Media.Info
				media.Status, media.Reason = archive.MediaFailed, "message deleted before the download finished"
			}
			h.route(ctx, e, chat, kind, sender, senderName, content, &media)
		},
	})
}

// route hands a new message to the deliverer when its chat is routed.
// Edits and revokes are not forwarded: agents react to new messages.
func (h *Handler) route(ctx context.Context, e *events.Message, chat types.JID, kind archive.ChatKind,
	sender types.JID, senderName string, content Content, media *archive.Media) {
	var routes Routes
	if h.Routes != nil {
		routes = h.Routes()
	}
	path := ""
	if h.Deliver != nil {
		path = routes.PathFor(chat.String())
	}
	publish := false
	if h.Publish != nil && h.Publishes != nil {
		publish = h.Publishes().Wants(chat.String(), func() bool {
			w, err := h.Archive.IsWatched(ctx, chat.String())
			return err == nil && w
		})
	}
	if (path == "" && !publish) || (e.Info.IsFromMe && !routes.IncludeOwn) {
		return
	}
	chatName := chat.String()
	if c, err := h.Archive.GetChat(ctx, chat.String()); err == nil {
		chatName = c.Name
	}
	pn, _ := h.Dir.Own()
	ev := messageEvent(e.Info.ID, newTraceID(), sender.String(), senderName, pn.String(), chat.String(), chatName, kind,
		e.Info.IsFromMe, content.Kind, content.Text, content.ReplyTo, e.Info.Timestamp, media)
	if path != "" {
		h.Deliver(path, withMediaPath(ev, media))
	}
	if publish {
		h.Publish(ev)
	}
}

// ensureChat adds a chat seen for the first time to the catalog, so it can
// be found by name and watched. Known chats are left alone: names are
// refreshed in bulk on connect.
func (h *Handler) ensureChat(ctx context.Context, chat types.JID, kind archive.ChatKind, info types.MessageInfo) error {
	known, err := h.Archive.Known(ctx, chat.String())
	if err != nil || known {
		return err
	}
	var name string
	switch kind {
	case archive.KindGroup:
		name = h.Dir.GroupName(ctx, chat)
	case archive.KindSelf:
		name = selfChatName(h.Dir.OwnPushName())
	default:
		name = h.Dir.ContactName(ctx, chat)
		if name == "" && !info.IsFromMe {
			name = info.PushName
		}
	}
	return h.Archive.UpsertChat(ctx, chat.String(), name, kind)
}

func (h *Handler) archiveError(chatJID string, err error) {
	h.Log.Error(yardlogs.EventWAArchiveError, slog.String(yardlogs.KeyMessage, "archive write failed"),
		slog.String(yardlogs.KeyChatJID, chatJID), slog.String(yardlogs.KeyError, err.Error()))
}

// normalizeChat returns the stable identity of the chat a message belongs
// to, or ok=false for chats the archive does not track (status updates,
// broadcast lists, channels).
//
// WhatsApp increasingly addresses one-to-one chats by LID (a hidden user
// id) instead of the phone number. The catalog is keyed by phone-number
// JIDs — that is what contacts and the user know — so LIDs are mapped back
// whenever the mapping is available.
func (h *Handler) normalizeChat(ctx context.Context, src types.MessageSource) (types.JID, archive.ChatKind, bool) {
	chat := src.Chat.ToNonAD()
	switch chat.Server {
	case types.GroupServer:
		return chat, archive.KindGroup, true
	case types.DefaultUserServer, types.HiddenUserServer:
	default:
		return types.JID{}, "", false
	}
	if chat.Server == types.HiddenUserServer {
		alt := src.SenderAlt
		if src.IsFromMe {
			alt = src.RecipientAlt
		}
		chat = h.toPN(ctx, chat, alt)
	}
	if h.isOwn(chat) {
		pn, _ := h.Dir.Own()
		if !pn.IsEmpty() {
			chat = pn.ToNonAD()
		}
		return chat, archive.KindSelf, true
	}
	return chat, archive.KindDM, true
}

func (h *Handler) normalizeSender(ctx context.Context, src types.MessageSource) types.JID {
	if src.IsFromMe {
		if pn, _ := h.Dir.Own(); !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	sender := src.Sender.ToNonAD()
	if sender.Server == types.HiddenUserServer {
		alt := types.JID{}
		if !src.IsFromMe {
			alt = src.SenderAlt
		}
		sender = h.toPN(ctx, sender, alt)
	}
	return sender
}

// toPN maps a LID to a phone-number JID using the message's alternate
// address first and the local LID store second. Unmappable LIDs are kept.
func (h *Handler) toPN(ctx context.Context, lid, alt types.JID) types.JID {
	if alt.Server == types.DefaultUserServer {
		return alt.ToNonAD()
	}
	if pn, ok := h.Dir.PNForLID(ctx, lid); ok {
		return pn.ToNonAD()
	}
	return lid
}

func (h *Handler) isOwn(j types.JID) bool {
	pn, lid := h.Dir.Own()
	return (!pn.IsEmpty() && j.User == pn.User && j.Server == pn.Server) ||
		(!lid.IsEmpty() && j.User == lid.User && j.Server == lid.Server)
}

// ContactName is a display name and where it came from.
type ContactName struct {
	Name string
	// Saved is true for names from the phone's address book, which win
	// over push (profile) and business names.
	Saved bool
}

// betterThan orders candidate names for one chat: saved beats unsaved,
// then the lexically smaller name, so the choice never depends on map order.
func (c ContactName) betterThan(o ContactName) bool {
	if c.Saved != o.Saved {
		return c.Saved
	}
	return c.Name < o.Name
}

func selfChatName(pushName string) string {
	if pushName == "" {
		return "Me (message yourself)"
	}
	return pushName + " (message yourself)"
}

// onHistorySync harvests chat names from the initial sync so `chats` and
// `watch <name>` work right after pairing. The messages themselves are
// discarded: nothing is archived for a chat before it is watched.
func (h *Handler) onHistorySync(ctx context.Context, e *events.HistorySync) {
	n := 0
	for _, conv := range e.Data.GetConversations() {
		jid, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		chat, kind, ok := h.normalizeChat(ctx, types.MessageSource{Chat: jid})
		if !ok {
			continue
		}
		// For one-to-one chats the address-book name wins: the sync's
		// conversation name there is the other person's push name, and
		// sync chunks keep arriving after the catalog refresh, so letting
		// it through would rename "Vini Eduqo" to whatever Vini calls
		// himself. Groups have no address-book name; use the subject.
		var name string
		switch kind {
		case archive.KindSelf:
			name = selfChatName(h.Dir.OwnPushName())
		case archive.KindDM:
			name = h.Dir.ContactName(ctx, chat)
		}
		if name == "" {
			name = conv.GetName()
		}
		if name == "" {
			name = conv.GetDisplayName()
		}
		if err := h.Archive.UpsertChat(ctx, chat.String(), name, kind); err != nil {
			h.archiveError(chat.String(), err)
			continue
		}
		if ts := max(conv.GetConversationTimestamp(), conv.GetLastMsgTimestamp()); ts > 0 {
			if err := h.Archive.TouchActivity(ctx, chat.String(), time.Unix(int64(ts), 0)); err != nil {
				h.archiveError(chat.String(), err)
			}
		}
		n++
	}
	h.Log.Info(yardlogs.EventWAHistorySync, slog.String(yardlogs.KeyMessage, "history sync: chats catalogued"),
		slog.Int(yardlogs.KeyCount, n))
}

// RefreshCatalog upserts every saved contact, every joined group and the
// self chat, so names track renames made on the phone.
func (h *Handler) RefreshCatalog(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	n := 0
	contacts, err := h.Dir.Contacts(ctx)
	if err != nil {
		h.archiveError("", fmt.Errorf("list contacts: %w", err))
	}
	// Merge entries that resolve to the same chat before writing: a LID
	// entry usually carries only the push name, and letting map order pick
	// the winner made names flip between restarts.
	best := map[types.JID]ContactName{}
	for jid, name := range contacts {
		chat, kind, ok := h.normalizeChat(ctx, types.MessageSource{Chat: jid})
		if !ok || kind == archive.KindSelf || chat.Server != types.DefaultUserServer {
			continue
		}
		if cur, seen := best[chat]; !seen || name.betterThan(cur) {
			best[chat] = name
		}
	}
	for chat, name := range best {
		if err := h.Archive.UpsertChat(ctx, chat.String(), name.Name, archive.KindDM); err == nil {
			n++
		}
	}
	groups, err := h.Dir.Groups(ctx)
	if err != nil {
		h.archiveError("", fmt.Errorf("list groups: %w", err))
	}
	for jid, name := range groups {
		if err := h.Archive.UpsertChat(ctx, jid.ToNonAD().String(), name, archive.KindGroup); err == nil {
			n++
		}
	}
	if pn, _ := h.Dir.Own(); !pn.IsEmpty() {
		if err := h.Archive.UpsertChat(ctx, pn.ToNonAD().String(), selfChatName(h.Dir.OwnPushName()), archive.KindSelf); err == nil {
			n++
		}
	}
	h.Log.Info(yardlogs.EventWACatalogRefreshed, slog.String(yardlogs.KeyMessage, "chat catalog refreshed"),
		slog.Int(yardlogs.KeyCount, n))
}
