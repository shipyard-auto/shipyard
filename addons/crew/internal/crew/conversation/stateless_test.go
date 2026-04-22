package conversation

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

func TestStatelessResolveReturnsEmpty(t *testing.T) {
	s := NewStateless()
	cases := []struct {
		name  string
		agent *crew.Agent
		input map[string]any
	}{
		{"nil agent and input", nil, nil},
		{"empty agent", &crew.Agent{}, map[string]any{}},
		{"populated", &crew.Agent{Name: "bot"}, map[string]any{"x": 1, "y": "z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := s.Resolve(tc.agent, tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key != "" {
				t.Fatalf("expected empty key, got %q", key)
			}
		})
	}
}

func TestStatelessLoadReturnsEmptyHistory(t *testing.T) {
	s := NewStateless()
	h, err := s.Load(context.Background(), &crew.Agent{Name: "bot"}, "any")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Messages != nil {
		t.Fatalf("expected nil Messages, got %#v", h.Messages)
	}
	if h.SessionID != "" {
		t.Fatalf("expected empty SessionID, got %q", h.SessionID)
	}
}

func TestStatelessSaveIsNoOp(t *testing.T) {
	s := NewStateless()
	h := History{
		SessionID: "sid-1",
		Messages: []Message{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
			{Role: "assistant", Content: json.RawMessage(`"hi"`)},
		},
	}
	for i := 0; i < 100; i++ {
		if err := s.Save(context.Background(), &crew.Agent{Name: "bot"}, "key", h); err != nil {
			t.Fatalf("iter %d: unexpected error: %v", i, err)
		}
	}
	got, err := s.Load(context.Background(), &crew.Agent{Name: "bot"}, "key")
	if err != nil {
		t.Fatalf("load after save: %v", err)
	}
	if got.SessionID != "" || got.Messages != nil {
		t.Fatalf("stateless leaked state: %#v", got)
	}
}

func TestStatelessImplementsStore(t *testing.T) {
	storeType := reflect.TypeOf((*Store)(nil)).Elem()
	if !reflect.TypeOf(NewStateless()).Implements(storeType) {
		t.Fatalf("*Stateless does not implement Store")
	}
}

func TestStatelessIgnoresCancelledContext(t *testing.T) {
	s := NewStateless()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Load(ctx, &crew.Agent{Name: "bot"}, "k"); err != nil {
		t.Fatalf("Load should ignore ctx: %v", err)
	}
	if err := s.Save(ctx, &crew.Agent{Name: "bot"}, "k", History{}); err != nil {
		t.Fatalf("Save should ignore ctx: %v", err)
	}
}
