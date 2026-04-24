package logs

import (
	"context"
	"log/slog"
	"sync"

	"github.com/shipyard-auto/shipyard/internal/metadata"
)

var (
	defaultStoreOnce sync.Once
	defaultStore     *Store
	defaultStoreErr  error
)

// DefaultStore returns a process-wide Store rooted at the canonical
// shipyard logs directory (~/.shipyard/logs). The first call resolves the
// path; subsequent calls reuse the singleton so file descriptors are
// shared across producers in the same binary.
func DefaultStore() (*Store, error) {
	defaultStoreOnce.Do(func() {
		shipyardHome, err := metadata.DefaultHomeDir()
		if err != nil {
			defaultStoreErr = err
			return
		}
		defaultStore = NewStore(shipyardHome + "/logs")
	})
	return defaultStore, defaultStoreErr
}

// DefaultLogger returns a *slog.Logger writing to DefaultStore under the
// given source. On filesystem errors it returns a logger with a nop
// handler so producers never need to handle the nil case.
func DefaultLogger(source string) *slog.Logger {
	store, err := DefaultStore()
	if err != nil || store == nil {
		return slog.New(NopHandler())
	}
	return New(source, Options{Store: store})
}

// NopHandler returns an slog.Handler that swallows every record. Useful
// in tests and as a fallback when the filesystem cannot be opened.
func NopHandler() slog.Handler { return nopHandler{} }

type nopHandler struct{}

func (nopHandler) Enabled(_ context.Context, _ slog.Level) bool   { return false }
func (nopHandler) Handle(_ context.Context, _ slog.Record) error  { return nil }
func (nopHandler) WithAttrs(_ []slog.Attr) slog.Handler           { return nopHandler{} }
func (nopHandler) WithGroup(_ string) slog.Handler                { return nopHandler{} }
