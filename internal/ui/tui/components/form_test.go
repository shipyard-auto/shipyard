package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestFormCompletes(t *testing.T) {
	in := NewInput(theme.New(), "Name", "", nil)
	in.SetValue("Backup")
	form := NewForm(theme.New(), []FormStep{{ID: "name", Label: "Name", Field: &in}})
	if !strings.Contains(form.View(), "Step 1 of 1") {
		t.Fatalf("expected progress in form view: %q", form.View())
	}
	_, cmd := form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd().(FormCompletedMsg)
	if msg.Values["name"] != "Backup" {
		t.Fatalf("unexpected values: %#v", msg.Values)
	}
}
