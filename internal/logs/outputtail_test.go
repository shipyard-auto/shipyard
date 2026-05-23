package logs

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTail_shorterThanLimit_returnsAsIs(t *testing.T) {
	got := Tail("hello", 100)
	if got != "hello" {
		t.Fatalf("Tail = %q, want %q", got, "hello")
	}
}

func TestTail_equalToLimit_returnsAsIs(t *testing.T) {
	in := strings.Repeat("x", 4096)
	got := Tail(in, 4096)
	if got != in {
		t.Fatalf("Tail with len == n should return input unchanged")
	}
}

func TestTail_longerThanLimit_returnsLastNBytes(t *testing.T) {
	in := strings.Repeat("a", 5000) + "END"
	got := Tail(in, 100)
	if len(got) > 100 {
		t.Fatalf("len(Tail) = %d, want ≤ 100", len(got))
	}
	if !strings.HasSuffix(got, "END") {
		t.Fatalf("Tail should preserve the final bytes, got suffix %q", got[len(got)-3:])
	}
}

func TestTail_nonPositiveN_returnsAsIs(t *testing.T) {
	got := Tail("hello", 0)
	if got != "hello" {
		t.Fatalf("n=0 should be a no-op, got %q", got)
	}
	got = Tail("hello", -1)
	if got != "hello" {
		t.Fatalf("negative n should be a no-op, got %q", got)
	}
}

// TestTail_utf8_preservesRuneBoundary protects against the regression where
// Tail returns invalid UTF-8 by slicing mid-codepoint. "ção" encodes the
// ç as 0xC3 0xA7 (two bytes) and ã as 0xC3 0xA3 (two bytes); cutting
// between them must walk forward, not return a leading 0xA7.
func TestTail_utf8_preservesRuneBoundary(t *testing.T) {
	in := strings.Repeat("a", 100) + "função concluída"
	for n := 1; n <= len(in); n++ {
		got := Tail(in, n)
		if !utf8.ValidString(got) {
			t.Fatalf("Tail(_, %d) returned invalid UTF-8: %q", n, got)
		}
	}
}

func TestTail_defaultBudget_isReasonable(t *testing.T) {
	if DefaultOutputTailBytes <= 0 || DefaultOutputTailBytes > 64*1024 {
		t.Fatalf("DefaultOutputTailBytes = %d looks wrong (want > 0 and ≤ 64 KiB)", DefaultOutputTailBytes)
	}
}
