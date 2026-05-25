package comms

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ChannelType identifies the provider family of a channel. Each known
// type maps (eventually) to one provider package under
// addons/comms/internal/comms/provider/<type>/.
//
// Adding a new provider = add a constant here and extend
// knownChannelTypes. Validation accepts the new type automatically.
type ChannelType string

const (
	ChannelTypeTelegram ChannelType = "telegram"
	// Future: ChannelTypeSlack, ChannelTypeWhatsApp, ChannelTypeEmail.
)

// knownChannelTypes lists every type this build of comms understands.
// Kept private and exposed via KnownChannelTypes() so callers can't
// accidentally mutate the slice.
var knownChannelTypes = []ChannelType{
	ChannelTypeTelegram,
}

// IsKnown reports whether t is in the set of types this build of comms
// recognizes. Used by Channel.Validate to fail fast on unsupported
// configurations.
func (t ChannelType) IsKnown() bool {
	for _, k := range knownChannelTypes {
		if k == t {
			return true
		}
	}
	return false
}

// KnownChannelTypes returns the list of types this build supports, in a
// stable order. Returns a fresh copy on every call to keep the internal
// catalog immutable from outside the package.
func KnownChannelTypes() []ChannelType {
	out := make([]ChannelType, len(knownChannelTypes))
	copy(out, knownChannelTypes)
	return out
}

// channelIDPattern: 16-32 chars, lowercase alphanumeric. Compatible with
// crockford-base32 ULIDs but doesn't require ULID specifically — any
// generator that produces matching strings is fine. The CLI in épico 2
// will be the one picking a generator.
var channelIDPattern = regexp.MustCompile(`^[a-z0-9]{16,32}$`)

// channelNamePattern: 3-40 chars, starts with letter, then letters /
// digits / hyphens / underscores. No leading or trailing punctuation.
// Same shape used by crew agent names so users carry one mental model
// across subsystems.
var channelNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{2,39}$`)

// Channel is a configured connection to one external messaging provider.
// Channels are persisted in ~/.shipyard/comms/channels.json (handled by
// CM-04) and referenced by ID throughout the subsystem.
//
// Credentials are NOT stored on Channel. They live in a separate file
// keyed by ID under ~/.shipyard/comms/secrets/<id>.json; see CM-04.
type Channel struct {
	ID        string         `json:"id"`                 // ULID-like, immutable after creation
	Name      string         `json:"name"`               // human-friendly, unique across all channels
	Type      ChannelType    `json:"type"`               //
	Settings  map[string]any `json:"settings,omitempty"` // provider-specific opaque blob
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Validate enforces channel invariants:
//   - ID matches the channelIDPattern (16-32 lowercase alphanumeric)
//   - Name matches the channelNamePattern (3-40 chars, letter start)
//   - Type is one of KnownChannelTypes()
//   - CreatedAt is non-zero and CreatedAt <= UpdatedAt
//
// Settings is NOT validated here — provider-specific schema validation
// is the provider package's job (épico 3).
func (c Channel) Validate() error {
	if c.ID == "" {
		return errInvalidChannel("", "id is required")
	}
	if !channelIDPattern.MatchString(c.ID) {
		return errInvalidChannel(c.ID, fmt.Sprintf("id %q does not match %s", c.ID, channelIDPattern.String()))
	}
	if c.Name == "" {
		return errInvalidChannel(c.ID, "name is required")
	}
	if !channelNamePattern.MatchString(c.Name) {
		return errInvalidChannel(c.ID, fmt.Sprintf("name %q does not match %s", c.Name, channelNamePattern.String()))
	}
	if c.Type == "" {
		return errInvalidChannel(c.ID, "type is required")
	}
	if !c.Type.IsKnown() {
		return errInvalidChannel(c.ID, fmt.Sprintf("type %q is not supported by this build", c.Type))
	}
	if c.CreatedAt.IsZero() {
		return errInvalidChannel(c.ID, "created_at is required")
	}
	if c.UpdatedAt.Before(c.CreatedAt) {
		return errInvalidChannel(c.ID, "updated_at must not be before created_at")
	}
	return nil
}

// errInvalidChannel formats validation errors consistently. When id is
// empty (the failure is the ID itself) we omit the prefix.
func errInvalidChannel(id, msg string) error {
	if id == "" {
		return fmt.Errorf("comms: channel: %s", msg)
	}
	return fmt.Errorf("comms: channel %s: %s", id, msg)
}

// FindDuplicateName returns the first pair of channels in cs that share
// the same Name (case-insensitive), or (nil, nil) if all names are
// unique. Comparison is case-insensitive because users will alternate
// casing without intending to create two distinct channels.
//
// The pair is returned in the order they appear in cs — first is the
// earlier occurrence, second is the later. Only the first pair is
// reported; if a third channel shares the name, it's ignored.
func FindDuplicateName(cs []Channel) (first, second *Channel) {
	seen := make(map[string]int, len(cs))
	for i := range cs {
		key := strings.ToLower(cs[i].Name)
		if prev, ok := seen[key]; ok {
			return &cs[prev], &cs[i]
		}
		seen[key] = i
	}
	return nil, nil
}
