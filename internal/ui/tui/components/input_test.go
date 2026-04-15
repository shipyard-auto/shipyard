package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestInputRendersAndValidates(t *testing.T) {
	in := NewInput(theme.New(), "Name", "job", func(v string) error {
		if strings.TrimSpace(v) == "" {
			return assertErr("required")
		}
		return nil
	})
	view := in.View()
	if !strings.Contains(view, "Name") {
		t.Fatalf("expected label in input view: %q", view)
	}
	_, _ = in.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(in.View(), "required") {
		t.Fatalf("expected validation error: %q", in.View())
	}
}

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

func assertErr(s string) error { return simpleErr(s) }
