package signing

import (
	"fmt"
	"regexp"
	"strings"
)

// Scopes are opaque permission strings, "<name>[:<resource>]", where name
// is dotted ("whatsapp.read", "events.subscribe", "cron.run") and '*' is a
// wildcard in either part. Shipyard's gateway only compares them; what a
// resource means (a chat, a job, a topic) is up to the service behind the
// route, which receives the client's scopes and narrows further.

var scopeNameRe = regexp.MustCompile(`^[a-z*][a-z0-9_.*-]{0,63}$`)

// ValidateScope checks the syntax of a scope or required-scope string.
func ValidateScope(s string) error {
	name, res, hasRes := strings.Cut(s, ":")
	if !scopeNameRe.MatchString(name) {
		return fmt.Errorf("scope %q: name must be lower-case dotted words (e.g. whatsapp.read), '*' allowed", s)
	}
	if hasRes && (res == "" || len(res) > 256 || strings.ContainsAny(res, " \t\n,")) {
		return fmt.Errorf("scope %q: resource must be 1-256 chars without spaces or commas", s)
	}
	return nil
}

// Grants reports whether a client holding any of grants may use a route
// that requires `required`.
//
//   - The names must match (globs on the grant side).
//   - A required scope without a resource ("whatsapp.read") is satisfied by
//     any grant of that name, whatever its resource: the service behind the
//     route enforces the resource itself.
//   - A required resource ("cron.run:backup") must match the grant's
//     resource glob; a grant without a resource covers every resource.
func Grants(grants []string, required string) bool {
	rName, rRes, rHas := strings.Cut(required, ":")
	for _, g := range grants {
		gName, gRes, gHas := strings.Cut(g, ":")
		if !Glob(gName, rName) {
			continue
		}
		if !rHas || !gHas || Glob(gRes, rRes) {
			return true
		}
	}
	return false
}

// Glob matches s against pattern, where '*' matches any run of characters
// (including none) and everything else is literal.
func Glob(pattern, s string) bool {
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
