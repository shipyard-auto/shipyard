package components

import (
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestEmptyRendersText(t *testing.T) {
	e := NewEmpty(theme.New(), EmptyProps{Icon: "⛵", Title: "No jobs yet.", Description: "Add one.", Hint: "[esc] back"})
	view := e.View()
	if !strings.Contains(view, "No jobs yet.") {
		t.Fatalf("expected title in empty view: %q", view)
	}
}
