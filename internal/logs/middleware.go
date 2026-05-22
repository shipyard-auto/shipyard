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
			ctx := trace.WithID(r.Context(), id)
			w.Header().Set(HeaderTraceID, id)

			start := time.Now()
			metrics := httpsnoop.CaptureMetrics(next, w, r.WithContext(ctx))

			logger.LogAttrs(ctx, slog.LevelInfo, EventHTTPRequest,
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
