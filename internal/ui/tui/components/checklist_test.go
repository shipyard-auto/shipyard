package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestChecklistUpdateAndEmit(t *testing.T) {
	cl := NewChecklist(theme.New(), []ChecklistItem{{ID: "A", Title: "Alpha"}})
	if !strings.Contains(cl.View(), "Alpha") {
		t.Fatal("expected checklist title")
	}
	cl, _ = cl.Update(tea.KeyMsg{Type: tea.KeySpace})
	_, cmd := cl.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd().(ChecklistConfirmedMsg)
	if len(msg.SelectedIDs) != 1 || msg.SelectedIDs[0] != "A" {
		t.Fatalf("unexpected selection: %#v", msg)
	}
}
