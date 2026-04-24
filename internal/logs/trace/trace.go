// Package trace propagates a per-request trace identifier through
// context.Context. The slog handler in internal/logs reads the value
// automatically via Handle, so callers only need to call WithID once at
// the entry point of a request and forward the resulting context.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type contextKey struct{}

// WithID returns a copy of ctx that carries id as the active trace id.
// Empty ids are stored as-is so that downstream readers can distinguish
// "explicitly cleared" from "absent" via Lookup.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// ID returns the trace id stored in ctx, or "" if none.
func ID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(contextKey{}).(string)
	return v
}

// NewID returns a random 16-char hex identifier.
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
