package logs

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/felixge/httpsnoop"

	"github.com/shipyard-auto/shipyard/internal/logs/trace"
)

// HeaderTraceID is the canonical HTTP header used to receive an inbound
// trace id and to echo it on responses.
const HeaderTraceID = "X-Trace-Id"

// unmatchedKey is the context key under which Middleware stores the
// per-request unmatched flag.
type unmatchedKey struct{}

// unmatchedFlag is a mutable cell handed to the downstream handler via
// context. The middleware reads it after the handler returns to decide the
// log level for the request.
type unmatchedFlag struct{ set bool }

// MarkUnmatched signals to the logging middleware that this request did not
// match any registered route. The middleware uses this hint, together with a
// 404 response, to demote the structured log entry from INFO to DEBUG. The
// goal is to keep the operator-facing log readable on a fairway exposed to
// the public internet, where automated bot scans produce a long tail of 404s
// against paths like /wp-login.php or /.env.
//
// A 404 from a *matched* route (e.g. the agent target itself answered 404)
// is still logged at INFO, because that is operator-relevant signal.
//
// Safe to call from any goroutine spawned synchronously from the handler,
// but the flag is read only after the handler returns. Calling outside of a
// request handled by Middleware is a no-op.
func MarkUnmatched(r *http.Request) {
	if f, ok := r.Context().Value(unmatchedKey{}).(*unmatchedFlag); ok {
		f.set = true
	}
}

// Middleware returns an http.Handler middleware that:
//
//   - reads or generates a trace id and stores it in request context;
//   - echoes that trace id on the response under HeaderTraceID;
//   - emits one structured log entry per completed request, with
//     duration, status, response size and remote address.
//
// httpsnoop is used to wrap the ResponseWriter so optional interfaces
// (Flusher, Hijacker, Pusher, ReaderFrom) keep working.
func Middleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderTraceID)
			if id == "" {
				id = trace.NewID()
			}
			flag := &unmatchedFlag{}
			ctx := trace.WithID(r.Context(), id)
			ctx = context.WithValue(ctx, unmatchedKey{}, flag)
			w.Header().Set(HeaderTraceID, id)

			start := time.Now()
			metrics := httpsnoop.CaptureMetrics(next, w, r.WithContext(ctx))

			level := slog.LevelInfo
			if flag.set && metrics.Code == http.StatusNotFound {
				level = slog.LevelDebug
			}
			logger.LogAttrs(ctx, level, EventHTTPRequest,
				slog.String(KeyHTTPMethod, r.Method),
				slog.String(KeyHTTPPath, r.URL.Path),
				slog.Int(KeyHTTPStatus, metrics.Code),
				slog.Int64(KeyHTTPResponseSz, metrics.Written),
				slog.String(KeyHTTPRemoteAddr, clientIP(r)),
				slog.Int64(KeyDurationMs, time.Since(start).Milliseconds()),
			)
		})
	}
}

// EnsureTraceID returns ctx with a trace id, generating one if absent.
// Useful at the top of a non-HTTP entry point (cron tick, CLI command).
func EnsureTraceID(ctx context.Context) context.Context {
	if trace.ID(ctx) != "" {
		return ctx
	}
	return trace.WithID(ctx, trace.NewID())
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	return r.RemoteAddr
}
