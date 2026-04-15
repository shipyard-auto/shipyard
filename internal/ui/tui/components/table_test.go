package components

import (
	"strings"
	"testing"

	btable "github.com/charmbracelet/bubbles/table"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestTableRenderSmoke(t *testing.T) {
	tb := NewTable(theme.New(),
		[]btable.Column{{Title: "ID", Width: 8}},
		[]btable.Row{{"AB12CD"}},
	)
	if !strings.Contains(tb.View(), "AB12CD") {
		t.Fatalf("expected row in table view: %q", tb.View())
	}
}
