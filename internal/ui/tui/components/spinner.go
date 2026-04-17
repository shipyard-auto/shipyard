package components

import (
	bspin "github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type Spinner struct {
	theme theme.Theme
	model bspin.Model
	label string
	width int
}

func NewSpinner(th theme.Theme, label string) Spinner {
	sp := bspin.New()
	sp.Spinner = bspin.MiniDot
	if th.ColorEnabled {
		sp.Style = th.ValueStyle.Foreground(th.Accent)
	}
	return Spinner{theme: th, model: sp, label: label}
}

func (s Spinner) Init() tea.Cmd { return s.model.Tick }

func (s *Spinner) Resize(width, _ int) { s.width = width }

func (s Spinner) Update(msg tea.Msg) (Spinner, tea.Cmd) {
	if resize, ok := msg.(tea.WindowSizeMsg); ok {
		s.Resize(resize.Width, resize.Height)
	}
	var cmd tea.Cmd
	s.model, cmd = s.model.Update(msg)
	return s, cmd
}

func (s Spinner) View() string {
	return s.model.View() + " " + s.label
}
