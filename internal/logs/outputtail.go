package logs

import "unicode/utf8"

// DefaultOutputTailBytes is the byte budget Shipyard subprocess runners
// (cron, crew, future) use when emitting output_tail on completion events.
// 4 KiB is enough to surface the final error/summary of typical stdouts
// while keeping individual JSONL lines from blowing past sane sizes.
const DefaultOutputTailBytes = 4096

// Tail returns the last n bytes of s, or s itself if it is shorter. When the
// cut would land in the middle of a multi-byte UTF-8 rune, the start advances
// forward to the next rune boundary so the returned string is always valid
// UTF-8 — slog encoders otherwise emit replacement chars and the JSONL line
// becomes harder to read.
//
// Tail is intentionally simple (no allocations beyond the slice header):
// callers already hold the full output buffer, so no streaming/ring-buffer
// machinery is needed. If a future caller streams a process's stdout with
// bounded memory, build a separate writer wrapper on top of this helper.
func Tail(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
