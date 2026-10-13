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
	// ContactName is the best local name for a user: saved contact name,
	// then push name, then business name. Empty when unknown.
	ContactName(ctx context.Context, jid types.JID) string
	// GroupName fetches the subject of a group (may hit the network).
	GroupName(ctx context.Context, jid types.JID) string
	// Contacts lists every user in the device's contact store with a name.
	Contacts(ctx context.Context) (map[types.JID]string, error)
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
}

// Fatal errors passed to OnFatal.
var (
	ErrLoggedOut      = fmt.Errorf("the phone removed this linked device; run 'shipyard comms whatsapp pair' again")
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
	case *events.Connected:
		h.Tracker.SetState(StateConnected, "")
		pn, _ := h.Dir.Own()
		h.Tracker.SetAccount(pn.User, h.Dir.OwnPushName())
		h.Log.Info("connected to WhatsApp", slog.String(yardlogs.KeyEvent, yardlogs.EventWAConnected))
		h.async(func() { h.RefreshCatalog(ctx) })
	case *events.Disconnected:
		h.Tracker.SetState(StateDisconnected, "")
		h.Log.Warn("disconnected from WhatsApp; reconnecting", slog.String(yardlogs.KeyEvent, yardlogs.EventWADisconnected))
	case *events.KeepAliveTimeout:
		h.Tracker.SetState(StateDisconnected, fmt.Sprintf("keepalive timeout (%d errors)", e.ErrorCount))
	case *events.KeepAliveRestored:
		h.Tracker.SetState(StateConnected, "")
	case *events.LoggedOut:
		h.Tracker.SetState(StateLoggedOut, e.Reason.String())
		h.Log.Error("logged out by the phone", slog.String(yardlogs.KeyEvent, yardlogs.EventWALoggedOut),
			slog.String(yardlogs.KeyError, e.Reason.String()))
		h.fatal(ErrLoggedOut)
	case *events.StreamReplaced:
		h.Tracker.SetState(StateStreamReplace, "")
		h.Log.Error("session replaced by another client", slog.String(yardlogs.KeyEvent, yardlogs.EventWAStreamReplaced))
		h.fatal(ErrStreamReplaced)
	case *events.TemporaryBan:
		h.Tracker.SetState(StateTemporaryBan, e.String())
		h.Log.Error("account temporarily banned", slog.String(yardlogs.KeyEvent, yardlogs.EventWATemporaryBan),
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
		_, err = h.Archive.InsertMessage(ctx, archive.Message{
			ChatJID:    chatJID,
			ID:         e.Info.ID,
			SenderJID:  sender.String(),
			SenderName: name,
			FromMe:     e.Info.IsFromMe,
			Timestamp:  e.Info.Timestamp,
			Kind:       content.Kind,
			Text:       content.Text,
			ReplyTo:    content.ReplyTo,
		})
	case ActionEdit:
		err = h.Archive.EditMessage(ctx, chatJID, content.TargetID, content.Kind, content.Text)
	case ActionRevoke:
		err = h.Archive.DeleteMessage(ctx, chatJID, content.TargetID)
	}
	if err != nil {
		h.archiveError(chatJID, err)
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
	h.Log.Error("archive write failed", slog.String(yardlogs.KeyEvent, yardlogs.EventWAArchiveError),
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
		name := conv.GetName()
		if name == "" {
			name = conv.GetDisplayName()
		}
		if name == "" && kind == archive.KindDM {
			name = h.Dir.ContactName(ctx, chat)
		}
		if kind == archive.KindSelf {
			name = selfChatName(h.Dir.OwnPushName())
		}
		if err := h.Archive.UpsertChat(ctx, chat.String(), name, kind); err != nil {
			h.archiveError(chat.String(), err)
			continue
		}
		n++
	}
	h.Log.Info("history sync: chats catalogued", slog.String(yardlogs.KeyEvent, yardlogs.EventWAHistorySync),
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
	for jid, name := range contacts {
		chat, kind, ok := h.normalizeChat(ctx, types.MessageSource{Chat: jid})
		if !ok || kind == archive.KindSelf || chat.Server != types.DefaultUserServer {
			continue
		}
		if err := h.Archive.UpsertChat(ctx, chat.String(), name, kind); err == nil {
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
	h.Log.Info("chat catalog refreshed", slog.String(yardlogs.KeyEvent, yardlogs.EventWACatalogRefreshed),
		slog.Int(yardlogs.KeyCount, n))
}
