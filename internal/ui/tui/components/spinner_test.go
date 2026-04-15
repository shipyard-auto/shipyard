package components

import (
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestSpinnerView(t *testing.T) {
	sp := NewSpinner(theme.New(), "Loading")
	if !strings.Contains(sp.View(), "Loading") {
		t.Fatalf("expected label in spinner view: %q", sp.View())
	}
}
