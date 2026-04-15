package components

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type EmptyProps struct {
	Icon        string
	Title       string
	Description string
	Hint        string
}

type Empty struct {
	theme theme.Theme
	props EmptyProps
	width int
}

func NewEmpty(th theme.Theme, props EmptyProps) Empty {
	return Empty{theme: th, props: props}
}

func (e Empty) Init() tea.Cmd { return nil }

func (e *Empty) Resize(width, _ int) { e.width = width }

func (e Empty) Update(msg tea.Msg) (Empty, tea.Cmd) {
	if resize, ok := msg.(tea.WindowSizeMsg); ok {
		e.Resize(resize.Width, resize.Height)
	}
	return e, nil
}

func (e Empty) View() string {
	lines := []string{
		e.theme.SubtitleStyle.Render("⎯ ⎯ ⎯"),
		e.theme.TitleStyle.Render(e.props.Icon),
		e.theme.ValueStyle.Render(e.props.Title),
	}
	if e.props.Description != "" {
		lines = append(lines, e.theme.SubtitleStyle.Render(e.props.Description))
	}
	if e.props.Hint != "" {
		lines = append(lines, e.theme.RenderHint(e.props.Hint))
	}
	block := strings.Join(lines, "\n")
	if e.width > 0 {
		return lipgloss.PlaceHorizontal(e.width, lipgloss.Center, block)
	}
	return block
}
