package tty

import (
	"bytes"
	"strings"
	"testing"
)

func TestRequireTTYReturnsErrorForNonTTY(t *testing.T) {
	old := StdinFD
	t.Cleanup(func() { StdinFD = old })
	StdinFD = func() uintptr { return 0 }

	var stderr bytes.Buffer
	err := RequireTTY(nil, &stderr)
	if err == nil {
		t.Fatal("expected non-interactive error")
	}
	if !strings.Contains(stderr.String(), "This command requires an interactive terminal.") {
		t.Fatalf("expected user-facing message, got %q", stderr.String())
	}
}
