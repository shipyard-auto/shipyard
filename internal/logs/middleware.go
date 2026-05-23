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

// responseBodyKey is the context key under which Middleware stores a
// per-request holder that handlers can populate with the response body via
// MarkResponseBody. The middleware reads the holder after the handler
// returns to attach output_tail to the http_request log line.
type responseBodyKey struct{}

// responseBodyHolder is a mutable cell handed to the downstream handler via
// context. Handlers call MarkResponseBody to set the bytes; the middleware
// reads .bytes after the handler returns. The holder is per-request, so
// concurrent requests do not share state. A nil holder (request not handled
// by Middleware) makes MarkResponseBody a no-op.
type responseBodyHolder struct{ bytes []byte }

// MarkResponseBody lets a handler signal to the logging middleware that the
// given bytes were the effective response body for this request, so they
// can be surfaced as output_tail on http_request. Pass result.Body or
// equivalent — only the last DefaultOutputTailBytes are kept on the log
// line.
//
// Safe to call from any goroutine spawned synchronously from the handler,
// but the holder is read only after the handler returns. Calling outside
// of a request handled by Middleware is a no-op.
func MarkResponseBody(r *http.Request, body []byte) {
	if h, ok := r.Context().Value(responseBodyKey{}).(*responseBodyHolder); ok {
		h.bytes = body
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
			holder := &responseBodyHolder{}
			ctx := trace.WithID(r.Context(), id)
			ctx = context.WithValue(ctx, responseBodyKey{}, holder)
			w.Header().Set(HeaderTraceID, id)

			start := time.Now()
			metrics := httpsnoop.CaptureMetrics(next, w, r.WithContext(ctx))

			attrs := []slog.Attr{
				slog.String(KeyHTTPMethod, r.Method),
				slog.String(KeyHTTPPath, r.URL.Path),
				slog.Int(KeyHTTPStatus, metrics.Code),
				slog.Int64(KeyHTTPResponseSz, metrics.Written),
				slog.String(KeyHTTPRemoteAddr, clientIP(r)),
				slog.Int64(KeyDurationMs, time.Since(start).Milliseconds()),
			}
			if len(holder.bytes) > 0 {
				attrs = append(attrs,
					slog.String(KeyOutputTail, Tail(string(holder.bytes), DefaultOutputTailBytes)),
					slog.Bool(KeyOutputTruncated, len(holder.bytes) > DefaultOutputTailBytes),
				)
			}
			logger.LogAttrs(ctx, slog.LevelInfo, EventHTTPRequest, attrs...)
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
