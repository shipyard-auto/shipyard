package comms

import (
	"errors"
	"fmt"
	"time"
)

// MessageSchemaVersion is the current envelope contract version. Bump on
// any incompatible change to Message, Address, Part or any sub-type.
// Compatibility policy: a new version must keep one prior version readable
// for one release cycle before the old code is removed.
const MessageSchemaVersion = 1

// Direction is the flow of a Message relative to the local Shipyard
// installation. Inbound means the message arrived from a provider;
// outbound means it is being sent to one.
type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

// IsValid reports whether d is one of the recognized directions. Comparison
// is exact (case-sensitive, no trimming) — providers and clients must use
// the canonical lowercase forms.
func (d Direction) IsValid() bool {
	switch d {
	case DirectionInbound, DirectionOutbound:
		return true
	default:
		return false
	}
}

// PartKind classifies a non-text fragment of a message: media, file
// attachment, or structured payload like a location.
type PartKind string

const (
	PartText     PartKind = "text"
	PartImage    PartKind = "image"
	PartAudio    PartKind = "audio"
	PartVideo    PartKind = "video"
	PartFile     PartKind = "file"
	PartLocation PartKind = "location"
)

// IsValid reports whether k is one of the recognized part kinds.
func (k PartKind) IsValid() bool {
	switch k {
	case PartText, PartImage, PartAudio, PartVideo, PartFile, PartLocation:
		return true
	default:
		return false
	}
}

// isMediaKind reports whether a Part of this kind carries binary payload
// (URL or Data). Text and location kinds do not.
func (k PartKind) isMediaKind() bool {
	switch k {
	case PartImage, PartAudio, PartVideo, PartFile:
		return true
	default:
		return false
	}
}

// Address identifies a participant inside a given channel. Raw is the
// channel-native identifier (chat_id for Telegram, e-mail address for
// SMTP, phone E.164 for WhatsApp). Display is an optional human label.
type Address struct {
	Channel string `json:"channel"`           // channel ID (matches Channel.ID)
	Raw     string `json:"raw"`               // provider-native identifier
	Display string `json:"display,omitempty"` // optional human-readable name
}

// Validate returns nil if Address is well-formed (Channel and Raw
// non-empty). Display is optional and not validated.
func (a Address) Validate() error {
	if a.Channel == "" {
		return errors.New("comms: address: channel is required")
	}
	if a.Raw == "" {
		return errors.New("comms: address: raw is required")
	}
	return nil
}

// Part is one attachment or structured fragment of a message. Body text
// lives on Message.Body; Parts holds everything else (images, files,
// locations). For media kinds (image/audio/video/file) exactly one of
// URL or Data must be set.
type Part struct {
	Kind     PartKind `json:"kind"`
	MimeType string   `json:"mime_type,omitempty"`
	URL      string   `json:"url,omitempty"`  // external storage reference
	Data     []byte   `json:"data,omitempty"` // inline payload (base64 in JSON)
	Filename string   `json:"filename,omitempty"`
}

// Validate enforces:
//   - Kind is one of the recognized PartKind values
//   - For media kinds, exactly one of URL or Data is set
//
// Text and location parts are accepted without payload constraints — the
// caller is responsible for putting text content on Message.Body and
// location data in Metadata or Filename per the channel convention.
func (p Part) Validate() error {
	if !p.Kind.IsValid() {
		return fmt.Errorf("comms: part: unknown kind %q", string(p.Kind))
	}
	if p.Kind.isMediaKind() {
		hasURL := p.URL != ""
		hasData := len(p.Data) > 0
		switch {
		case !hasURL && !hasData:
			return fmt.Errorf("comms: part: %s requires url or data", p.Kind)
		case hasURL && hasData:
			return fmt.Errorf("comms: part: %s must set url or data, not both", p.Kind)
		}
	}
	return nil
}

// Message is the normalized envelope used across the comms subsystem,
// agnostic of provider. It crosses process boundaries as JSON; the
// SchemaVersion field gates compatibility for future evolutions.
//
// Required fields when constructing a Message: SchemaVersion, Channel,
// Direction, From, at least one To, Timestamp, and either Body or one
// Part with content.
type Message struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id,omitempty"`       // provider's message id
	TraceID       string            `json:"trace_id,omitempty"` // log correlation
	Channel       string            `json:"channel"`            // channel ID
	Direction     Direction         `json:"direction"`
	From          Address           `json:"from"`
	To            []Address         `json:"to"`
	Subject       string            `json:"subject,omitempty"` // e-mail-like channels
	Body          string            `json:"body"`
	Parts         []Part            `json:"parts,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"` // provider-specific extras
	Timestamp     time.Time         `json:"ts"`
}

// Validate enforces every invariant of the Message envelope. It is
// intentionally strict: a message that round-trips through JSON and
// passes Validate is safe to hand to any downstream consumer in this
// or any future build of comms (modulo SchemaVersion bumps).
func (m Message) Validate() error {
	if m.SchemaVersion != MessageSchemaVersion {
		return fmt.Errorf("comms: message: schema_version %d not supported (this build expects %d)",
			m.SchemaVersion, MessageSchemaVersion)
	}
	if m.Channel == "" {
		return errors.New("comms: message: channel is required")
	}
	if !m.Direction.IsValid() {
		return fmt.Errorf("comms: message: unknown direction %q", string(m.Direction))
	}
	if err := m.From.Validate(); err != nil {
		return fmt.Errorf("comms: message: from: %w", err)
	}
	if len(m.To) == 0 {
		return errors.New("comms: message: to must contain at least one address")
	}
	for i, to := range m.To {
		if err := to.Validate(); err != nil {
			return fmt.Errorf("comms: message: to[%d]: %w", i, err)
		}
	}
	if m.Body == "" && len(m.Parts) == 0 {
		return errors.New("comms: message: body or at least one part is required")
	}
	for i, part := range m.Parts {
		if err := part.Validate(); err != nil {
			return fmt.Errorf("comms: message: parts[%d]: %w", i, err)
		}
	}
	if m.Timestamp.IsZero() {
		return errors.New("comms: message: ts is required")
	}
	return nil
}
