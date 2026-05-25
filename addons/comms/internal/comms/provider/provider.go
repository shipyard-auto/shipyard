// Package provider defines the contract every messaging provider in the
// shipyard-comms addon must satisfy, plus a small Registry to dispatch
// channels to their owning provider at runtime.
//
// v1 is send-only. Inbound traffic for webhook-based providers reaches the
// caller through fairway (HTTP front door), so providers do not need a
// receive method.
package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
)

// Receipt is the per-provider acknowledgement of a successful send.
//
// ProviderMessageID is the native identifier the provider assigned to the
// outgoing message (Telegram's message_id, e-mail Message-ID, etc.) and is
// useful for downstream correlation. SentAt is when the provider accepted
// the message, in UTC; it may differ from Message.Timestamp (which is when
// the caller built the envelope).
type Receipt struct {
	ProviderMessageID string
	SentAt            time.Time
}

// Provider is implemented by every messaging backend the addon supports.
// Implementations must be safe for concurrent use — the Registry hands out
// the same instance to every caller in the same process.
type Provider interface {
	// Name returns the provider's lowercase identifier, matching the
	// corresponding comms.ChannelType (e.g. "telegram").
	Name() string

	// Send delivers msg through the provider, using channel for any
	// non-secret configuration and secret for credentials. The secret is
	// the raw bytes stored under ~/.shipyard/comms/secrets/<channel-id>.json;
	// the implementation owns the credential schema and is responsible
	// for parsing it.
	Send(ctx context.Context, channel comms.Channel, secret []byte, msg comms.Message) (Receipt, error)
}

// ErrUnknownType is wrapped by Registry.Lookup when no provider is
// registered for the requested channel type. Callers use errors.Is to
// detect this and surface an actionable message.
var ErrUnknownType = errors.New("no provider registered for channel type")

// Registry is a goroutine-safe lookup from comms.ChannelType to Provider.
// The zero value is NOT usable — construct one via NewRegistry.
type Registry struct {
	mu        sync.RWMutex
	providers map[comms.ChannelType]Provider
}

// NewRegistry returns an empty Registry ready for Register calls.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[comms.ChannelType]Provider)}
}

// Register associates p with t. Subsequent Register calls for the same
// type overwrite the previous binding (useful for test injection).
//
// Register panics when p is nil — registering a nil provider would defer
// every call site's NPE to runtime, hiding the bug.
func (r *Registry) Register(t comms.ChannelType, p Provider) {
	if p == nil {
		panic(fmt.Sprintf("provider: refuse to Register nil provider for type %q", t))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[t] = p
}

// Lookup returns the provider associated with t, or an error wrapping
// ErrUnknownType when no provider is registered.
func (r *Registry) Lookup(t comms.ChannelType) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[t]
	if !ok {
		return nil, fmt.Errorf("comms: %w: %q", ErrUnknownType, t)
	}
	return p, nil
}

// Types returns the channel types currently registered, in undefined
// order. Useful for CLI help text and for tests that want to assert the
// registry was populated as expected.
func (r *Registry) Types() []comms.ChannelType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]comms.ChannelType, 0, len(r.providers))
	for t := range r.providers {
		out = append(out, t)
	}
	return out
}
