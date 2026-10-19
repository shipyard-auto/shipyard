package wa

import (
	"encoding/hex"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
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
	// Media is set for image, video, audio, file and sticker messages.
	Media *MediaRef
}

// MediaRef is what the archive records about an attachment plus what is
// needed to fetch it. The download keys live only here, in memory: they
// are never written to disk.
type MediaRef struct {
	Info   archive.Media // metadata; Status is decided by the handler
	Source whatsmeow.DownloadableMessage
}

// mediaSource is the slice of a media proto both the metadata and the
// download need; every waE2E media message implements it.
type mediaSource interface {
	whatsmeow.DownloadableMessage
	GetMimetype() string
	GetFileLength() uint64
}

func newMediaRef(src mediaSource, info archive.Media) *MediaRef {
	info.MimeType = src.GetMimetype()
	info.Size = int64(src.GetFileLength())
	if sum := src.GetFileSHA256(); len(sum) > 0 {
		info.SHA256 = hex.EncodeToString(sum)
	}
	return &MediaRef{Info: info, Source: src}
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
		c.Media = newMediaRef(im, archive.Media{Width: int(im.GetWidth()), Height: int(im.GetHeight())})
	case m.GetVideoMessage() != nil:
		vm := m.GetVideoMessage()
		c.Kind, c.Text = "video", vm.GetCaption()
		c.ReplyTo = vm.GetContextInfo().GetStanzaID()
		c.Media = newMediaRef(vm, videoInfo(vm))
	case m.GetPtvMessage() != nil: // round "video note"
		vm := m.GetPtvMessage()
		c.Kind = "video"
		c.ReplyTo = vm.GetContextInfo().GetStanzaID()
		c.Media = newMediaRef(vm, videoInfo(vm))
	case m.GetAudioMessage() != nil:
		am := m.GetAudioMessage()
		c.Kind = "audio"
		c.ReplyTo = am.GetContextInfo().GetStanzaID()
		c.Media = newMediaRef(am, archive.Media{Seconds: int(am.GetSeconds()), VoiceNote: am.GetPTT()})
	case m.GetDocumentMessage() != nil:
		dm := m.GetDocumentMessage()
		c.Kind = "file"
		c.Text = joinNonEmpty(" — ", dm.GetFileName(), dm.GetCaption())
		c.ReplyTo = dm.GetContextInfo().GetStanzaID()
		c.Media = newMediaRef(dm, archive.Media{FileName: dm.GetFileName()})
	case m.GetStickerMessage() != nil:
		sm := m.GetStickerMessage()
		c.Kind = "sticker"
		c.Media = newMediaRef(sm, archive.Media{Width: int(sm.GetWidth()), Height: int(sm.GetHeight())})
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

func videoInfo(vm *waE2E.VideoMessage) archive.Media {
	return archive.Media{Seconds: int(vm.GetSeconds()), Width: int(vm.GetWidth()), Height: int(vm.GetHeight())}
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
