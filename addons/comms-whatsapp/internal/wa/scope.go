package wa

import (
	"strings"

	"github.com/shipyard-auto/shipyard/internal/signing"
)

// ReadScopeName is the scope that lets a client read chats through the
// query API and receive whatsapp.message events: "whatsapp.read:<chat jid>"
// for one chat, "whatsapp.read:120363*@g.us" for a pattern, plain
// "whatsapp.read" or "whatsapp.read:*" for every chat.
const ReadScopeName = "whatsapp.read"

// SendScopeName lets a client send through the query API, with the same
// resource forms: "whatsapp.send:<chat jid>", a glob, or every chat. It
// adds to the owner's per-chat allowlist and limits; it never bypasses them.
const SendScopeName = "whatsapp.send"

// coversAll reports whether scopes grant scopeName on every chat, so "no
// such chat" can be told apart from "not yours" only for such clients.
func coversAll(scopes []string, scopeName string) bool {
	for _, g := range scopes {
		name, res, has := strings.Cut(strings.TrimSpace(g), ":")
		if signing.Glob(name, scopeName) && (!has || res == "*") {
			return true
		}
	}
	return false
}
