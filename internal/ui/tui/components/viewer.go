package components

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type Viewer struct {
	theme theme.Theme
	model viewport.Model
}

func NewViewer(th theme.Theme, content string) Viewer {
	vp := viewport.New(80, 20)
	vp.SetContent(content)
	return Viewer{theme: th, model: vp}
}

func (v Viewer) Init() tea.Cmd { return nil }

func (v *Viewer) Resize(width, height int) {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 10
	}
	v.model.Width = width
	v.model.Height = height
}

func (v *Viewer) SetContent(content string) {
	v.model.SetContent(content)
}

func (v Viewer) Update(msg tea.Msg) (Viewer, tea.Cmd) {
	if resize, ok := msg.(tea.WindowSizeMsg); ok {
		v.Resize(resize.Width, resize.Height)
	}
	var cmd tea.Cmd
	v.model, cmd = v.model.Update(msg)
	return v, cmd
}

func (v Viewer) View() string { return v.model.View() }
