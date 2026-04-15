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

func (e Empty) SetWidth(width int) Empty {
	e.width = width
	return e
}

func (e Empty) Update(msg tea.Msg) (Empty, tea.Cmd) {
	if resize, ok := msg.(tea.WindowSizeMsg); ok {
		e.width = resize.Width
	}
	return e, nil
}

func (e Empty) View() string {
	icon := e.props.Icon
	if strings.TrimSpace(icon) == "" {
		icon = "⛵"
	}

	iconStyle := lipgloss.NewStyle().Foreground(e.theme.Accent).Bold(true)
	titleStyle := e.theme.ValueStyle.Foreground(e.theme.Text)
	descStyle := e.theme.SubtitleStyle
	hintStyle := e.theme.HintStyle

	lines := []string{
		iconStyle.Render(icon),
		"",
		titleStyle.Render(e.props.Title),
	}
	if e.props.Description != "" {
		lines = append(lines, descStyle.Render(e.props.Description))
	}
	if e.props.Hint != "" {
		lines = append(lines, "", hintStyle.Render(theme.GlyphArrow+" "+e.props.Hint))
	}

	block := lipgloss.JoinVertical(lipgloss.Center, lines...)

	// Frame the empty state in a subtle rounded panel for clear visual grouping.
	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(e.theme.SurfaceAlt).
		Padding(1, 4).
		Render(block)

	if e.width > 0 {
		return lipgloss.PlaceHorizontal(e.width, lipgloss.Center, panel)
	}
	return panel
}
