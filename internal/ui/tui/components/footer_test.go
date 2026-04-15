package components

import (
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestFooterRenderSmoke(t *testing.T) {
	f := NewFooter(theme.New(), []KeyHint{{Key: "enter", Label: "select"}}, false)
	view := f.View()
	if !strings.Contains(view, "select") {
		t.Fatalf("expected key hint in footer view: %q", view)
	}
	if !strings.Contains(view, "esc") {
		t.Fatalf("expected esc hint in footer view: %q", view)
	}
}
