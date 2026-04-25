package logs

import (
	"context"
	"log/slog"
	"os"

	"github.com/shipyard-auto/shipyard/internal/app"
)

// Options control the construction of a *slog.Logger via New.
type Options struct {
	// Store receives the serialized JSONL lines. Required.
	Store *Store

	// Level is the minimum level emitted. Defaults to slog.LevelInfo.
	Level slog.Leveler

	// Version is exported as the service_version attribute. Defaults to
	// app.Version. Override when building loggers from addons that ship
	// their own version constant.
	Version string

	// Hostname is exported as the hostname attribute. Defaults to
	// os.Hostname(); errors are silently ignored.
	Hostname string

	// PID is exported as the pid attribute. Defaults to os.Getpid().
	PID int

	// Extra is composed in addition to the file Store; useful in dev to
	// also emit to stderr. Pass slog.NewTextHandler(os.Stderr, ...) etc.
	Extra slog.Handler

	// Sampler, when non-nil, decides whether a record is persisted. Returning
	// false drops the record before it reaches the Store. Nil keeps every
	// record (the default for production today). Wired up but unused — kept
	// here so callers can opt-in without a downstream API change.
	Sampler Sampler
}

// Sampler decides whether a record should be persisted. Implementations must
// be safe for concurrent use.
type Sampler interface {
	Keep(ctx context.Context, r slog.Record) bool
}

// SamplerFunc adapts a plain function to the Sampler interface.
type SamplerFunc func(ctx context.Context, r slog.Record) bool

func (f SamplerFunc) Keep(ctx context.Context, r slog.Record) bool { return f(ctx, r) }

// New builds a *slog.Logger that writes to the configured Store under the
// given source name. Standard host/process attributes are baked in.
func New(source string, opts Options) *slog.Logger {
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}
	if opts.Hostname == "" {
		if h, err := os.Hostname(); err == nil {
			opts.Hostname = h
		}
	}
	if opts.PID == 0 {
		opts.PID = os.Getpid()
	}
	if opts.Version == "" {
		opts.Version = app.Version
	}

	var h slog.Handler = NewHandler(opts.Store, source, opts.Level)
	if opts.Sampler != nil {
		h = samplingHandler{inner: h, sampler: opts.Sampler}
	}
	if opts.Extra != nil {
		h = extraMulti{primary: h, extra: opts.Extra}
	}

	return slog.New(h).With(
		slog.String(KeyHostname, opts.Hostname),
		slog.Int(KeyPID, opts.PID),
		slog.String(KeyServiceVersion, opts.Version),
	)
}

// extraMulti fans out a record to two handlers. Errors from extra are
// swallowed because the primary store is the source of truth; a broken
// stderr should not stop production logging.
type extraMulti struct {
	primary slog.Handler
	extra   slog.Handler
}

func (m extraMulti) Enabled(ctx context.Context, lvl slog.Level) bool {
	return m.primary.Enabled(ctx, lvl) || m.extra.Enabled(ctx, lvl)
}

func (m extraMulti) Handle(ctx context.Context, r slog.Record) error {
	if m.extra != nil {
		_ = m.extra.Handle(ctx, r.Clone())
	}
	return m.primary.Handle(ctx, r)
}

func (m extraMulti) WithAttrs(attrs []slog.Attr) slog.Handler {
	return extraMulti{
		primary: m.primary.WithAttrs(attrs),
		extra:   m.extra.WithAttrs(attrs),
	}
}

func (m extraMulti) WithGroup(name string) slog.Handler {
	return extraMulti{
		primary: m.primary.WithGroup(name),
		extra:   m.extra.WithGroup(name),
	}
}

// samplingHandler drops records for which the configured Sampler returns
// false. Enabled is delegated to the inner handler so callers still see the
// same level filter regardless of sampling.
type samplingHandler struct {
	inner   slog.Handler
	sampler Sampler
}

func (s samplingHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return s.inner.Enabled(ctx, lvl)
}

func (s samplingHandler) Handle(ctx context.Context, r slog.Record) error {
	if !s.sampler.Keep(ctx, r) {
		return nil
	}
	return s.inner.Handle(ctx, r)
}

func (s samplingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return samplingHandler{inner: s.inner.WithAttrs(attrs), sampler: s.sampler}
}

func (s samplingHandler) WithGroup(name string) slog.Handler {
	return samplingHandler{inner: s.inner.WithGroup(name), sampler: s.sampler}
}
