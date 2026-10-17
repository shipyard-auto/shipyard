package wa

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

// ResolveChat finds a catalog chat by JID, by name (case- and
// accent-insensitive; several matches yield *archive.AmbiguousError) or by
// phone number in any common notation. Used by the CLI and by the daemon's
// send, so every client can address chats the same way.
func ResolveChat(ctx context.Context, arch *archive.Store, query string) (archive.Chat, error) {
	chat, err := arch.Resolve(ctx, query)
	if !errors.Is(err, archive.ErrNotFound) {
		return chat, err
	}
	if jid, ok := PhoneJID(query); ok {
		if c, err := arch.GetChat(ctx, jid); err == nil {
			return c, nil
		}
	}
	return chat, err
}

// PhoneJID turns "+55 (11) 99999-9999" into "5511999999999@s.whatsapp.net".
func PhoneJID(s string) (string, bool) {
	var digits strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			digits.WriteRune(r)
		case strings.ContainsRune("+ -().", r):
		default:
			return "", false
		}
	}
	d := digits.String()
	if len(d) < 8 || len(d) > 15 || strings.HasPrefix(d, "0") {
		return "", false
	}
	return d + "@s.whatsapp.net", true
}
