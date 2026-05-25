package provider

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
)

// stubProvider is a Provider that records calls without making any real
// I/O — used to exercise the Registry without a network dependency.
type stubProvider struct {
	name  string
	calls int
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Send(_ context.Context, _ comms.Channel, _ []byte, _ comms.Message) (Receipt, error) {
	s.calls++
	return Receipt{ProviderMessageID: "fake-" + s.name}, nil
}

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	tg := &stubProvider{name: "telegram"}
	r.Register(comms.ChannelTypeTelegram, tg)

	got, err := r.Lookup(comms.ChannelTypeTelegram)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != tg {
		t.Errorf("Lookup returned a different instance: got %p want %p", got, tg)
	}
}

func TestRegistry_Lookup_UnknownType(t *testing.T) {
	r := NewRegistry()
	_, err := r.Lookup(comms.ChannelType("slack"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrUnknownType) {
		t.Errorf("expected errors.Is(err, ErrUnknownType), got %v", err)
	}
}

func TestRegistry_Register_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic registering nil provider, got none")
		}
	}()
	NewRegistry().Register(comms.ChannelTypeTelegram, nil)
}

func TestRegistry_Register_Overwrites(t *testing.T) {
	r := NewRegistry()
	first := &stubProvider{name: "first"}
	second := &stubProvider{name: "second"}
	r.Register(comms.ChannelTypeTelegram, first)
	r.Register(comms.ChannelTypeTelegram, second)

	got, err := r.Lookup(comms.ChannelTypeTelegram)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != second {
		t.Errorf("expected second registration to win, got %v", got)
	}
}

func TestRegistry_Types_ListsRegisteredKinds(t *testing.T) {
	r := NewRegistry()
	r.Register(comms.ChannelTypeTelegram, &stubProvider{name: "telegram"})
	// Register a second made-up type to exercise multi-entry handling.
	r.Register(comms.ChannelType("slack"), &stubProvider{name: "slack"})

	got := r.Types()
	sort.Slice(got, func(i, j int) bool { return string(got[i]) < string(got[j]) })

	want := []comms.ChannelType{"slack", "telegram"}
	if len(got) != len(want) {
		t.Fatalf("Types(): got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Types()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRegistry_ConcurrentReadWrite(t *testing.T) {
	r := NewRegistry()
	r.Register(comms.ChannelTypeTelegram, &stubProvider{name: "telegram"})

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Lookup(comms.ChannelTypeTelegram); err != nil {
				t.Errorf("Lookup: %v", err)
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Register(comms.ChannelTypeTelegram, &stubProvider{name: "telegram"})
		}()
	}
	wg.Wait()
}
