package comms

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fixedTS gives every test a deterministic, non-zero timestamp in UTC.
var fixedTS = time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

func minimalValidMessage() Message {
	return Message{
		SchemaVersion: MessageSchemaVersion,
		Channel:       "chan-1",
		Direction:     DirectionOutbound,
		From:          Address{Channel: "chan-1", Raw: "+5511999999999"},
		To:            []Address{{Channel: "chan-1", Raw: "+5511888888888"}},
		Body:          "hello",
		Timestamp:     fixedTS,
	}
}

func TestMessage_RoundTrip_Minimal(t *testing.T) {
	original := minimalValidMessage()

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(original, decoded) {
		t.Errorf("round-trip mismatch\noriginal=%+v\ndecoded=%+v", original, decoded)
	}
}

func TestMessage_RoundTrip_Full(t *testing.T) {
	original := Message{
		SchemaVersion: MessageSchemaVersion,
		ID:            "tg-987654",
		TraceID:       "trace-abc",
		Channel:       "chan-1",
		Direction:     DirectionInbound,
		From:          Address{Channel: "chan-1", Raw: "12345", Display: "Alice"},
		To: []Address{
			{Channel: "chan-1", Raw: "bot1", Display: "Bot"},
			{Channel: "chan-1", Raw: "67890"},
		},
		Subject: "Re: stuff",
		Body:    "with attachments",
		Parts: []Part{
			{Kind: PartImage, MimeType: "image/png", URL: "https://example.com/x.png"},
			{Kind: PartFile, MimeType: "application/pdf", Data: []byte("PDF-bytes"), Filename: "doc.pdf"},
			{Kind: PartLocation, MimeType: "application/geo+json", Filename: "geo"},
		},
		Metadata: map[string]string{
			"telegram.chat.type": "private",
			"telegram.update_id": "42",
		},
		Timestamp: fixedTS,
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(original, decoded) {
		t.Errorf("round-trip mismatch\noriginal=%+v\ndecoded=%+v", original, decoded)
	}
}

func TestMessage_JSON_OmitsZeroFields(t *testing.T) {
	m := minimalValidMessage()

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	s := string(raw)
	for _, field := range []string{"\"id\"", "\"trace_id\"", "\"subject\"", "\"parts\"", "\"metadata\""} {
		if strings.Contains(s, field) {
			t.Errorf("expected %s to be omitted from minimal JSON, got %s", field, s)
		}
	}
	// Required fields must still be present.
	for _, field := range []string{"\"schema_version\"", "\"channel\"", "\"direction\"", "\"from\"", "\"to\"", "\"body\"", "\"ts\""} {
		if !strings.Contains(s, field) {
			t.Errorf("expected %s to be present in JSON, got %s", field, s)
		}
	}
}

func TestMessage_Validate_HappyPath(t *testing.T) {
	if err := minimalValidMessage().Validate(); err != nil {
		t.Errorf("minimal valid message rejected: %v", err)
	}
}

func TestMessage_Validate_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Message)
		wantSub string
	}{
		{"wrong schema version", func(m *Message) { m.SchemaVersion = 999 }, "schema_version"},
		{"empty channel", func(m *Message) { m.Channel = "" }, "channel is required"},
		{"unknown direction", func(m *Message) { m.Direction = Direction("sideways") }, "direction"},
		{"empty from raw", func(m *Message) { m.From.Raw = "" }, "from"},
		{"empty to slice", func(m *Message) { m.To = nil }, "to must contain"},
		{"to with empty raw", func(m *Message) { m.To = []Address{{Channel: "chan-1"}} }, "to[0]"},
		{"zero timestamp", func(m *Message) { m.Timestamp = time.Time{} }, "ts is required"},
		{"empty body and no parts", func(m *Message) { m.Body = ""; m.Parts = nil }, "body or at least one part"},
		{"invalid part kind", func(m *Message) {
			m.Parts = []Part{{Kind: PartKind("hologram")}}
		}, "parts[0]"},
		{"media part with both url and data", func(m *Message) {
			m.Body = "" // force parts to be required
			m.Parts = []Part{{Kind: PartImage, URL: "https://x", Data: []byte("y")}}
		}, "must set url or data, not both"},
		{"media part with neither url nor data", func(m *Message) {
			m.Body = ""
			m.Parts = []Part{{Kind: PartImage}}
		}, "requires url or data"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := minimalValidMessage()
			tc.mutate(&m)
			err := m.Validate()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain expected substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestMessage_SchemaVersion_FailsForFuture(t *testing.T) {
	raw := []byte(`{
		"schema_version": 999,
		"channel": "chan-1",
		"direction": "outbound",
		"from": {"channel":"chan-1","raw":"a"},
		"to":   [{"channel":"chan-1","raw":"b"}],
		"body": "hi",
		"ts":   "2026-05-24T12:00:00Z"
	}`)

	var m Message
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal must succeed for forward-compatible reads: %v", err)
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected validation to reject schema_version 999")
	}
	if !strings.Contains(err.Error(), "999") || !strings.Contains(err.Error(), "1") {
		t.Errorf("error message should mention both received (999) and expected (1) versions, got: %v", err)
	}
}

func TestDirection_IsValid(t *testing.T) {
	cases := []struct {
		in   Direction
		want bool
	}{
		{DirectionInbound, true},
		{DirectionOutbound, true},
		{Direction(""), false},
		{Direction("INBOUND"), false},
		{Direction("inbound "), false},
		{Direction("foo"), false},
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			if got := tc.in.IsValid(); got != tc.want {
				t.Errorf("Direction(%q).IsValid() = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestPartKind_IsValid(t *testing.T) {
	cases := []struct {
		in   PartKind
		want bool
	}{
		{PartText, true},
		{PartImage, true},
		{PartAudio, true},
		{PartVideo, true},
		{PartFile, true},
		{PartLocation, true},
		{PartKind(""), false},
		{PartKind("IMAGE"), false},
		{PartKind("file "), false},
		{PartKind("sticker"), false},
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			if got := tc.in.IsValid(); got != tc.want {
				t.Errorf("PartKind(%q).IsValid() = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestAddress_Validate(t *testing.T) {
	cases := []struct {
		name    string
		in      Address
		wantErr bool
	}{
		{"valid", Address{Channel: "c", Raw: "r"}, false},
		{"with display", Address{Channel: "c", Raw: "r", Display: "x"}, false},
		{"missing channel", Address{Raw: "r"}, true},
		{"missing raw", Address{Channel: "c"}, true},
		{"empty", Address{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestMessage_Validate_WrapsUnderlyingErrors guarantees that error chains
// propagate up so callers can errors.Is/As to find the root cause.
func TestMessage_Validate_WrapsUnderlyingErrors(t *testing.T) {
	m := minimalValidMessage()
	m.To = []Address{{Channel: "c"}} // missing Raw — propagated from Address.Validate

	err := m.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	// We don't expose a sentinel here; check at minimum that the wrap chain
	// is preserved (Unwrap returns non-nil).
	if errors.Unwrap(err) == nil {
		t.Errorf("expected wrapped error, got flat: %v", err)
	}
}
