package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestExtractContent(t *testing.T) {
	quoted := &waE2E.ContextInfo{StanzaID: proto.String("q1")}
	cases := []struct {
		name string
		in   *waE2E.Message
		want Content
	}{
		{"nil", nil, Content{Action: ActionIgnore}},
		{"plain text", text("oi"), Content{Action: ActionStore, Kind: "text", Text: "oi"}},
		{"reply", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("sim"), ContextInfo: quoted}},
			Content{Action: ActionStore, Kind: "text", Text: "sim", ReplyTo: "q1"}},
		{"image with caption", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("cardápio")}},
			Content{Action: ActionStore, Kind: "image", Text: "cardápio"}},
		{"voice note", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}},
			Content{Action: ActionStore, Kind: "audio"}},
		{"document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("nota.pdf"), Caption: proto.String("segue")}},
			Content{Action: ActionStore, Kind: "file", Text: "nota.pdf — segue"}},
		{"location", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{Name: proto.String("Petz"), Address: proto.String("Av. X")}},
			Content{Action: ActionStore, Kind: "location", Text: "Petz, Av. X"}},
		{"contact", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ana")}},
			Content{Action: ActionStore, Kind: "contact", Text: "Ana"}},
		{"poll", &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("pizza?")}},
			Content{Action: ActionStore, Kind: "poll", Text: "pizza?"}},
		{"reaction", &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍"), Key: &waCommon.MessageKey{ID: proto.String("m9")}}},
			Content{Action: ActionStore, Kind: "reaction", Text: "👍", ReplyTo: "m9"}},
		{"reaction removed", &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("")}},
			Content{Action: ActionIgnore}},
		{"sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, Content{Action: ActionStore, Kind: "sticker"}},
		{"key distribution only", &waE2E.Message{SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{}},
			Content{Action: ActionIgnore}},
		{"key distribution with text", &waE2E.Message{SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{}, Conversation: proto.String("oi")},
			Content{Action: ActionStore, Kind: "text", Text: "oi"}},
		{"revoke", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("m1")}}},
			Content{Action: ActionRevoke, TargetID: "m1"}},
		{"edit", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("m1")}, EditedMessage: text("v2")}},
			Content{Action: ActionEdit, Kind: "text", Text: "v2", TargetID: "m1"}},
		{"edit to nothing", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("m1")}}},
			Content{Action: ActionIgnore}},
		{"other protocol", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION.Enum()}},
			Content{Action: ActionIgnore}},
		{"unknown payload", &waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{}}, Content{Action: ActionStore, Kind: "other"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractContent(tc.in); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}
