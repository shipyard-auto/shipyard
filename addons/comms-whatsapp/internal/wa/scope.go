package wa

import "strings"

// ReadScopeName is the scope that lets a client read chats through the
// query API and receive whatsapp.message events: "whatsapp.read:<chat jid>"
// for one chat, "whatsapp.read:120363*@g.us" for a pattern, plain
// "whatsapp.read" or "whatsapp.read:*" for every chat.
const ReadScopeName = "whatsapp.read"

// grants reports whether any of the client's scopes covers required
// ("name:resource"). It mirrors internal/signing.Grants — the scope
// grammar is the contract between fairway and this daemon: '*' globs on
// the grant side, and a grant without a resource covers every resource.
func grants(scopes []string, required string) bool {
	rName, rRes, rHas := strings.Cut(required, ":")
	for _, g := range scopes {
		gName, gRes, gHas := strings.Cut(strings.TrimSpace(g), ":")
		if !glob(gName, rName) {
			continue
		}
		if !rHas || !gHas || glob(gRes, rRes) {
			return true
		}
	}
	return false
}

// readsEverything reports whether scopes cover every chat, so "no such
// chat" can be told apart from "not yours" only for such clients.
func readsEverything(scopes []string) bool {
	for _, g := range scopes {
		name, res, has := strings.Cut(strings.TrimSpace(g), ":")
		if glob(name, ReadScopeName) && (!has || res == "*") {
			return true
		}
	}
	return false
}

// glob matches s against pattern, where '*' matches any run of characters.
func glob(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}
