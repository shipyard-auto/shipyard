package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestConfirmEmitsMessage(t *testing.T) {
	c := NewConfirm(theme.New(), "Delete?", true)
	if !strings.Contains(c.View(), "Delete?") {
		t.Fatal("expected prompt")
	}
	c, _ = c.Update(tea.KeyMsg{Type: tea.KeyLeft})
	_, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd().(ConfirmMsg)
	if !msg.Accepted {
		t.Fatal("expected accepted confirm")
	}
}
