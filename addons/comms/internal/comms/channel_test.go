package comms

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

const validChannelID = "01htgkx7r4qx9z8m6n3v2bp4ay"

func validChannel() Channel {
	return Channel{
		ID:        validChannelID,
		Name:      "telegram-personal",
		Type:      ChannelTypeTelegram,
		CreatedAt: fixedTS,
		UpdatedAt: fixedTS,
	}
}

func TestChannelType_IsKnown(t *testing.T) {
	cases := []struct {
		in   ChannelType
		want bool
	}{
		{ChannelTypeTelegram, true},
		{ChannelType(""), false},
		{ChannelType("slack"), false},
		{ChannelType("TELEGRAM"), false},
		{ChannelType("telegram "), false},
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			if got := tc.in.IsKnown(); got != tc.want {
				t.Errorf("ChannelType(%q).IsKnown() = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestKnownChannelTypes_StableAndIsolated(t *testing.T) {
	first := KnownChannelTypes()
	second := KnownChannelTypes()
	if !reflect.DeepEqual(first, second) {
		t.Errorf("KnownChannelTypes not stable across calls: %v vs %v", first, second)
	}
	// v1 contract: telegram is the only supported type.
	want := []ChannelType{ChannelTypeTelegram}
	if !reflect.DeepEqual(first, want) {
		t.Errorf("KnownChannelTypes() = %v, want %v", first, want)
	}
	// Mutating the returned slice must not poison subsequent calls.
	first[0] = ChannelType("hacked")
	third := KnownChannelTypes()
	if !reflect.DeepEqual(third, want) {
		t.Errorf("external mutation leaked into catalog: got %v want %v", third, want)
	}
}

func TestChannel_Validate_HappyPath(t *testing.T) {
	if err := validChannel().Validate(); err != nil {
		t.Errorf("happy path channel rejected: %v", err)
	}
	// UpdatedAt strictly after CreatedAt should also pass.
	c := validChannel()
	c.UpdatedAt = c.CreatedAt.Add(time.Hour)
	if err := c.Validate(); err != nil {
		t.Errorf("updated_at > created_at rejected: %v", err)
	}
}

func TestChannel_Validate_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Channel)
		wantSub string
	}{
		{"empty id", func(c *Channel) { c.ID = "" }, "id is required"},
		{"uppercase id", func(c *Channel) { c.ID = "ABCDEFGHIJ123456" }, "does not match"},
		{"id too short", func(c *Channel) { c.ID = "abc123def4567890"[:15] }, "does not match"},
		{"id too long", func(c *Channel) { c.ID = strings.Repeat("a", 33) }, "does not match"},
		{"id with hyphen", func(c *Channel) { c.ID = "abc-defghij1234567" }, "does not match"},
		{"id with underscore", func(c *Channel) { c.ID = "abc_defghij1234567" }, "does not match"},
		{"id with space", func(c *Channel) { c.ID = "abc defghij1234567" }, "does not match"},
		{"empty name", func(c *Channel) { c.Name = "" }, "name is required"},
		{"name starts with digit", func(c *Channel) { c.Name = "1bot" }, "does not match"},
		{"name with space", func(c *Channel) { c.Name = "my bot" }, "does not match"},
		{"name too short", func(c *Channel) { c.Name = "ab" }, "does not match"},
		{"name too long", func(c *Channel) { c.Name = "a" + strings.Repeat("b", 40) }, "does not match"},
		{"empty type", func(c *Channel) { c.Type = ChannelType("") }, "type is required"},
		{"unknown type", func(c *Channel) { c.Type = ChannelType("slack") }, "not supported"},
		{"zero created_at", func(c *Channel) { c.CreatedAt = time.Time{} }, "created_at is required"},
		{"updated before created", func(c *Channel) { c.UpdatedAt = c.CreatedAt.Add(-time.Hour) }, "updated_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validChannel()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected error, got nil for: %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain expected substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestChannel_RoundTrip_JSON(t *testing.T) {
	original := Channel{
		ID:   validChannelID,
		Name: "tg-bot",
		Type: ChannelTypeTelegram,
		// Build Settings with values that round-trip cleanly through
		// encoding/json. Numbers come back as float64, so the original
		// is built with float64 to keep DeepEqual happy.
		Settings: map[string]any{
			"bot_username":    "mybot",
			"webhook_port":    float64(8443),
			"strict_https":    true,
			"allowed_updates": []any{"message", "edited_message"},
		},
		CreatedAt: fixedTS,
		UpdatedAt: fixedTS,
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Channel
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(original, decoded) {
		t.Errorf("round-trip mismatch\noriginal=%+v\ndecoded=%+v", original, decoded)
	}
}

func TestChannel_RoundTrip_OmitsEmptySettings(t *testing.T) {
	c := validChannel()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "\"settings\"") {
		t.Errorf("expected settings field to be omitted for nil map, got %s", raw)
	}
}

func TestFindDuplicateName(t *testing.T) {
	cases := []struct {
		name      string
		channels  []Channel
		wantFirst int // index in channels of expected first; -1 if no duplicate
	}{
		{
			name:      "empty",
			channels:  nil,
			wantFirst: -1,
		},
		{
			name: "no duplicates",
			channels: []Channel{
				{Name: "alpha"},
				{Name: "beta"},
				{Name: "gamma"},
			},
			wantFirst: -1,
		},
		{
			name: "exact duplicate",
			channels: []Channel{
				{Name: "alpha"},
				{Name: "beta"},
				{Name: "alpha"},
			},
			wantFirst: 0,
		},
		{
			name: "case-insensitive duplicate",
			channels: []Channel{
				{Name: "Bot"},
				{Name: "bot"},
			},
			wantFirst: 0,
		},
		{
			name: "three with same name returns first pair only",
			channels: []Channel{
				{Name: "x"},
				{Name: "x"},
				{Name: "x"},
			},
			wantFirst: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, second := FindDuplicateName(tc.channels)
			if tc.wantFirst < 0 {
				if first != nil || second != nil {
					t.Errorf("expected no duplicates, got (%v, %v)", first, second)
				}
				return
			}
			if first == nil || second == nil {
				t.Fatalf("expected duplicate pair, got (%v, %v)", first, second)
			}
			if first != &tc.channels[tc.wantFirst] {
				t.Errorf("first pointer = %p, want &channels[%d] = %p",
					first, tc.wantFirst, &tc.channels[tc.wantFirst])
			}
			// second must come after first in the slice (greater index).
			if second == first {
				t.Errorf("second must be a distinct element, got same as first")
			}
		})
	}
}
