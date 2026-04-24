package logs

import (
	"bytes"
	"context"
	"log/slog"
	"sync"

	"github.com/shipyard-auto/shipyard/internal/logs/trace"
)

// Handler is a slog.Handler that serializes records into JSONL via an
// internal slog.JSONHandler and routes the resulting line to a Store under
// a fixed source name.
//
// One Handler corresponds to one source. The source field is injected
// before the record is serialized so callers cannot accidentally override
// it. The trace id is read from context.Context on every Handle call.
type Handler struct {
	source string
	level  slog.Leveler
	store  *Store
	attrs  []slog.Attr
	groups []string

	bufPool sync.Pool
}

// NewHandler builds a Handler that writes to store under the given source.
// The level controls the slog filter; pass slog.LevelInfo for production.
func NewHandler(store *Store, source string, level slog.Leveler) *Handler {
	if level == nil {
		level = slog.LevelInfo
	}
	return &Handler{
		source: source,
		level:  level,
		store:  store,
		bufPool: sync.Pool{
			New: func() any { return &bytes.Buffer{} },
		},
	}
}

// Enabled reports whether the handler will emit records at level.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

// Handle writes r to the Store. It injects the configured source and the
// trace id (if any) lifted from ctx.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	rec := r.Clone()
	rec.AddAttrs(slog.String(KeySource, h.source))
	if id := trace.ID(ctx); id != "" {
		rec.AddAttrs(slog.String(KeyTraceID, id))
	}

	buf := h.bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer h.bufPool.Put(buf)

	enc := h.newEncoder(buf)
	if err := enc.Handle(ctx, rec); err != nil {
		return err
	}
	line := bytes.TrimRight(buf.Bytes(), "\n")
	out := make([]byte, len(line))
	copy(out, line)
	return h.store.Append(h.source, rec.Time, out)
}

// WithAttrs returns a new Handler with attrs preconfigured.
// The source binding is preserved.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		source: h.source,
		level:  h.level,
		store:  h.store,
		attrs:  append(append([]slog.Attr(nil), h.attrs...), attrs...),
		groups: append([]string(nil), h.groups...),
		bufPool: sync.Pool{
			New: func() any { return &bytes.Buffer{} },
		},
	}
}

// WithGroup is implemented for completeness but discouraged in this schema:
// callers should keep attrs flat.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		source: h.source,
		level:  h.level,
		store:  h.store,
		attrs:  append([]slog.Attr(nil), h.attrs...),
		groups: append(append([]string(nil), h.groups...), name),
		bufPool: sync.Pool{
			New: func() any { return &bytes.Buffer{} },
		},
	}
}

func (h *Handler) newEncoder(buf *bytes.Buffer) slog.Handler {
	var enc slog.Handler = slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level:       h.level,
		ReplaceAttr: replaceAttr,
	})
	if len(h.attrs) > 0 {
		enc = enc.WithAttrs(h.attrs)
	}
	for _, g := range h.groups {
		enc = enc.WithGroup(g)
	}
	return enc
}

// replaceAttr renames the canonical slog keys to the v2 schema (ts/event/
// message) and shapes the timestamp into RFC3339Nano UTC.
func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case slog.TimeKey:
		t := a.Value.Time()
		return slog.String(KeyTimestamp, t.UTC().Format("2006-01-02T15:04:05.000000Z07:00"))
	case slog.MessageKey:
		// slog uses one string for both event name and human message. We
		// treat record.Message as the event name (caller passes the const)
		// and surface it as "event". Free-form text goes into "message"
		// via an explicit attr.
		return slog.String(KeyEvent, a.Value.String())
	case slog.LevelKey:
		return slog.String(KeyLevel, a.Value.String())
	}
	return a
}
