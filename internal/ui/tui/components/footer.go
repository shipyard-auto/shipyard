package components

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type Footer struct {
	theme  theme.Theme
	hints  []KeyHint
	isRoot bool
	width  int
}

func NewFooter(th theme.Theme, hints []KeyHint, isRoot bool) Footer {
	return Footer{theme: th, hints: hints, isRoot: isRoot}
}

func (f Footer) Init() tea.Cmd { return nil }

func (f Footer) Update(msg tea.Msg) (Footer, tea.Cmd) {
	if resize, ok := msg.(tea.WindowSizeMsg); ok {
		f.Resize(resize.Width, resize.Height)
	}
	return f, nil
}

func (f *Footer) Resize(width, _ int) {
	f.width = width
}

func (f Footer) View() string {
	hints := append([]KeyHint{}, f.hints...)
	if !f.isRoot {
		hints = append([]KeyHint{{Key: "esc", Label: "back"}}, hints...)
	}
	hints = append(hints, KeyHint{Key: "q", Label: "quit"})

	parts := make([]string, 0, len(hints))
	for _, hint := range hints {
		parts = append(parts, f.theme.RenderKeyHint(hint.Key, hint.Label))
	}
	return strings.Join(parts, "  ")
}
