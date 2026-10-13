package wa

import (
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// Action says what an incoming message means for the archive.
type Action int

const (
	ActionIgnore Action = iota // protocol noise, key distribution, etc.
	ActionStore                // a new message
	ActionEdit                 // replaces the content of TargetID
	ActionRevoke               // "delete for everyone" of TargetID
)

// Content is the archive-relevant part of a WhatsApp message.
type Content struct {
	Action   Action
	Kind     string // text|image|audio|video|file|location|sticker|contact|poll|reaction|other
	Text     string // body, caption or a short description of the payload
	ReplyTo  string // quoted message ID, if any
	TargetID string // for ActionEdit / ActionRevoke
}

// ExtractContent classifies m, which must already be unwrapped (whatsmeow
// does that for events.Message: ephemeral, view-once and device-sent
// wrappers are removed).
func ExtractContent(m *waE2E.Message) Content {
	if m == nil {
		return Content{Action: ActionIgnore}
	}
	if pm := m.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			return Content{Action: ActionRevoke, TargetID: pm.GetKey().GetID()}
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			edited := ExtractContent(pm.GetEditedMessage())
			if edited.Action != ActionStore {
				return Content{Action: ActionIgnore}
			}
			edited.Action = ActionEdit
			edited.TargetID = pm.GetKey().GetID()
			return edited
		default:
			return Content{Action: ActionIgnore}
		}
	}

	c := Content{Action: ActionStore}
	switch {
	case m.GetConversation() != "":
		c.Kind, c.Text = "text", m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		et := m.GetExtendedTextMessage()
		c.Kind, c.Text = "text", et.GetText()
		c.ReplyTo = et.GetContextInfo().GetStanzaID()
	case m.GetImageMessage() != nil:
		im := m.GetImageMessage()
		c.Kind, c.Text = "image", im.GetCaption()
		c.ReplyTo = im.GetContextInfo().GetStanzaID()
	case m.GetVideoMessage() != nil:
		vm := m.GetVideoMessage()
		c.Kind, c.Text = "video", vm.GetCaption()
		c.ReplyTo = vm.GetContextInfo().GetStanzaID()
	case m.GetPtvMessage() != nil:
		c.Kind = "video"
		c.ReplyTo = m.GetPtvMessage().GetContextInfo().GetStanzaID()
	case m.GetAudioMessage() != nil:
		am := m.GetAudioMessage()
		c.Kind = "audio"
		c.ReplyTo = am.GetContextInfo().GetStanzaID()
	case m.GetDocumentMessage() != nil:
		dm := m.GetDocumentMessage()
		c.Kind = "file"
		c.Text = joinNonEmpty(" — ", dm.GetFileName(), dm.GetCaption())
		c.ReplyTo = dm.GetContextInfo().GetStanzaID()
	case m.GetStickerMessage() != nil:
		c.Kind = "sticker"
	case m.GetLocationMessage() != nil:
		lm := m.GetLocationMessage()
		c.Kind = "location"
		c.Text = joinNonEmpty(", ", lm.GetName(), lm.GetAddress())
	case m.GetLiveLocationMessage() != nil:
		c.Kind = "location"
		c.Text = m.GetLiveLocationMessage().GetCaption()
	case m.GetContactMessage() != nil:
		c.Kind, c.Text = "contact", m.GetContactMessage().GetDisplayName()
	case m.GetContactsArrayMessage() != nil:
		c.Kind, c.Text = "contact", m.GetContactsArrayMessage().GetDisplayName()
	case m.GetPollCreationMessage() != nil:
		c.Kind, c.Text = "poll", m.GetPollCreationMessage().GetName()
	case m.GetPollCreationMessageV2() != nil:
		c.Kind, c.Text = "poll", m.GetPollCreationMessageV2().GetName()
	case m.GetPollCreationMessageV3() != nil:
		c.Kind, c.Text = "poll", m.GetPollCreationMessageV3().GetName()
	case m.GetReactionMessage() != nil:
		rm := m.GetReactionMessage()
		if rm.GetText() == "" { // reaction removed
			return Content{Action: ActionIgnore}
		}
		c.Kind, c.Text = "reaction", rm.GetText()
		c.ReplyTo = rm.GetKey().GetID()
	case m.GetSenderKeyDistributionMessage() != nil, m.GetMessageContextInfo() != nil:
		// Reached only when none of the content cases matched: group key
		// distribution and bare context updates have no user-visible body.
		return Content{Action: ActionIgnore}
	default:
		c.Kind = "other"
	}
	return c
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
