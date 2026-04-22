package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSuccess(t *testing.T) {
	t.Run("nil data", func(t *testing.T) {
		e := Success(nil)
		if !e.Ok {
			t.Fatal("want Ok=true")
		}
		if len(e.Data) != 0 {
			t.Fatalf("want empty Data, got %s", e.Data)
		}
	})
	t.Run("struct", func(t *testing.T) {
		e := Success(map[string]any{"foo": "bar"})
		if !e.Ok {
			t.Fatal("want Ok=true")
		}
		if string(e.Data) != `{"foo":"bar"}` {
			t.Fatalf("want json, got %s", e.Data)
		}
	})
	t.Run("non-serializable falls back to failure", func(t *testing.T) {
		e := Success(make(chan int))
		if e.Ok {
			t.Fatal("want Ok=false")
		}
		if !strings.Contains(e.Error, "envelope: marshal data failed") {
			t.Fatalf("error = %q", e.Error)
		}
	})
}

func TestFailure(t *testing.T) {
	t.Run("msg only", func(t *testing.T) {
		e := Failure("boom", nil)
		if e.Ok {
			t.Fatal("want Ok=false")
		}
		if e.Error != "boom" {
			t.Fatalf("error = %q", e.Error)
		}
		if len(e.Details) != 0 {
			t.Fatalf("want empty Details")
		}
	})
	t.Run("msg and details", func(t *testing.T) {
		e := Failure("boom", map[string]any{"code": 2})
		if e.Ok {
			t.Fatal("want Ok=false")
		}
		if string(e.Details) != `{"code":2}` {
			t.Fatalf("details = %s", e.Details)
		}
	})
	t.Run("non-serializable details", func(t *testing.T) {
		e := Failure("boom", make(chan int))
		if e.Ok {
			t.Fatal("want Ok=false")
		}
		if !strings.Contains(e.Error, "details unserialized") {
			t.Fatalf("error = %q", e.Error)
		}
	})
	t.Run("empty msg is accepted but Parse rejects", func(t *testing.T) {
		e := Failure("", nil)
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := Parse(raw); err == nil {
			t.Fatal("expected Parse error for empty error message")
		}
	})
}

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
		check   func(t *testing.T, e Envelope)
	}{
		{
			name: "ok true with data",
			raw:  `{"ok":true,"data":{"x":1}}`,
			check: func(t *testing.T, e Envelope) {
				if !e.Ok || string(e.Data) != `{"x":1}` {
					t.Fatalf("unexpected: %+v", e)
				}
			},
		},
		{
			name: "ok false with error",
			raw:  `{"ok":false,"error":"boom"}`,
			check: func(t *testing.T, e Envelope) {
				if e.Ok || e.Error != "boom" {
					t.Fatalf("unexpected: %+v", e)
				}
			},
		},
		{
			name:    "ok false without error",
			raw:     `{"ok":false}`,
			wantErr: "envelope: ok=false requires error message",
		},
		{
			name:    "missing ok field",
			raw:     `{"error":"x"}`,
			wantErr: `envelope: missing required field "ok"`,
		},
		{
			name:    "empty payload",
			raw:     "",
			wantErr: "envelope: empty payload",
		},
		{
			name:    "whitespace only",
			raw:     "   \n\t ",
			wantErr: "envelope: empty payload",
		},
		{
			name:    "invalid json",
			raw:     "{ok:true}",
			wantErr: "envelope: invalid json",
		},
		{
			name:    "null raw",
			raw:     "null",
			wantErr: "envelope: missing required field",
		},
		{
			name:    "array raw",
			raw:     "[1,2,3]",
			wantErr: "envelope: invalid json",
		},
		{
			name: "extra fields ignored",
			raw:  `{"ok":true,"data":1,"extra":"ignored"}`,
			check: func(t *testing.T, e Envelope) {
				if !e.Ok || string(e.Data) != "1" {
					t.Fatalf("unexpected: %+v", e)
				}
			},
		},
		{
			name: "details as array preserved",
			raw:  `{"ok":false,"error":"x","details":[1,2]}`,
			check: func(t *testing.T, e Envelope) {
				if e.Ok || string(e.Details) != "[1,2]" {
					t.Fatalf("unexpected: %+v", e)
				}
			},
		},
		{
			name: "ok without data",
			raw:  `{"ok":true}`,
			check: func(t *testing.T, e Envelope) {
				if !e.Ok || len(e.Data) != 0 {
					t.Fatalf("unexpected: %+v", e)
				}
			},
		},
		{
			name:    "ok as string",
			raw:     `{"ok":"true"}`,
			wantErr: "envelope: decode struct",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Parse([]byte(c.raw))
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got nil", c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error = %q, want contains %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.check != nil {
				c.check(t, got)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		orig := Success(map[string]any{"foo": "bar", "n": 42})
		raw, err := json.Marshal(orig)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		parsed, err := Parse(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !parsed.Ok {
			t.Fatal("want Ok=true")
		}
		var got map[string]any
		if err := json.Unmarshal(parsed.Data, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got["foo"] != "bar" {
			t.Fatalf("lost data: %v", got)
		}
	})
	t.Run("failure", func(t *testing.T) {
		orig := Failure("boom", map[string]any{"code": 2})
		raw, err := json.Marshal(orig)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		parsed, err := Parse(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if parsed.Ok || parsed.Error != "boom" {
			t.Fatalf("unexpected: %+v", parsed)
		}
		if string(parsed.Details) != `{"code":2}` {
			t.Fatalf("details lost: %s", parsed.Details)
		}
	})
}
