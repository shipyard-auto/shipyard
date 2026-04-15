package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestMenuSelectionEmitsMessage(t *testing.T) {
	menu := NewMenu(theme.New(), []MenuItem{{Title: "Add", Key: "add"}, {Title: "Exit", Key: "exit"}})
	view := menu.View()
	if !strings.Contains(view, "Add") {
		t.Fatalf("expected menu item in view: %q", view)
	}
	_, cmd := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected selection cmd")
	}
	msg := cmd()
	sel, ok := msg.(MenuSelectedMsg)
	if !ok || sel.Key != "add" {
		t.Fatalf("unexpected selection msg: %#v", msg)
	}
}
