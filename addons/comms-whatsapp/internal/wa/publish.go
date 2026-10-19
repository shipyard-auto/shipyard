package wa

import (
	"fmt"
	"slices"
	"strings"
)

// Which chats' messages become whatsapp.message events. Publishing only
// what someone may consume keeps fairway (and its logs) free of traffic
// nobody receives, and makes 'watch' the single place to choose what is
// monitored.

// Publish selectors besides chat JIDs.
const (
	PublishWatched = "watched" // every watched chat, following watch/unwatch
	PublishAll     = "*"       // every chat the account is in
)

// PublishSelection lists PublishWatched, PublishAll and/or chat JIDs.
type PublishSelection []string

// Wants reports whether chatJID's messages are published. watched is
// asked only when the selection depends on it.
func (p PublishSelection) Wants(chatJID string, watched func() bool) bool {
	if slices.Contains(p, PublishAll) || slices.Contains(p, chatJID) {
		return true
	}
	return slices.Contains(p, PublishWatched) && watched != nil && watched()
}

// Enabled reports whether anything is published.
func (p PublishSelection) Enabled() bool { return len(p) > 0 }

// Validate checks every item.
func (p PublishSelection) Validate() error {
	for _, it := range p {
		if it != PublishAll && it != PublishWatched && !strings.Contains(it, "@") {
			return fmt.Errorf("%q is not a chat JID, %q or %q", it, PublishWatched, PublishAll)
		}
	}
	return nil
}

// Add appends item unless present; reports whether it changed.
func (p *PublishSelection) Add(item string) bool {
	if slices.Contains(*p, item) {
		return false
	}
	*p = append(*p, item)
	return true
}

// Remove drops item; reports whether it was there.
func (p *PublishSelection) Remove(item string) bool {
	i := slices.Index(*p, item)
	if i < 0 {
		return false
	}
	*p = slices.Delete(*p, i, i+1)
	return true
}

// migratePublish converts the pre-0.9.2 publish_events switch: true meant
// "every chat", but nothing outside watched chats was ever meant to be
// consumed, so it becomes the watched selector. Saving drops the old key.
func (c *Config) migratePublish() {
	if c.LegacyPublishEvents == nil {
		return
	}
	if *c.LegacyPublishEvents && len(c.Publish) == 0 {
		c.Publish = PublishSelection{PublishWatched}
	}
	c.LegacyPublishEvents = nil
}
