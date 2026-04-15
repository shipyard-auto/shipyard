package components

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type ConfirmMsg struct {
	Accepted bool
}

type Confirm struct {
	theme     theme.Theme
	prompt    string
	dangerous bool
	focus     int
	width     int
}

func NewConfirm(th theme.Theme, prompt string, dangerous bool) Confirm {
	focus := 0
	if dangerous {
		focus = 1
	}
	return Confirm{theme: th, prompt: prompt, dangerous: dangerous, focus: focus}
}

func (c Confirm) Init() tea.Cmd { return nil }

func (c *Confirm) Resize(width, _ int) { c.width = width }

func (c Confirm) Update(msg tea.Msg) (Confirm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.Resize(msg.Width, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "left", "right", "tab", "h", "l":
			c.focus = 1 - c.focus
		case "enter":
			return c, func() tea.Msg { return ConfirmMsg{Accepted: c.focus == 0} }
		case "esc":
			return c, func() tea.Msg { return ConfirmMsg{Accepted: false} }
		}
	}
	return c, nil
}

func (c Confirm) View() string {
	confirm := c.renderButton("Confirm", c.focus == 0, c.dangerous)
	cancel := c.renderButton("Cancel", c.focus == 1, false)
	buttons := lipgloss.JoinHorizontal(lipgloss.Top, confirm, "  ", cancel)

	return strings.Join([]string{
		c.prompt,
		"",
		buttons,
	}, "\n")
}

func (c Confirm) renderButton(label string, focused bool, destructive bool) string {
	prefix := "  "
	if focused {
		prefix = theme.GlyphSelected + " "
	}
	text := prefix + label + "  "

	if !c.theme.ColorEnabled {
		style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
		if focused {
			style = style.Bold(true)
		}
		return style.Render(text)
	}

	if focused {
		bg := c.theme.Accent
		if destructive && label == "Confirm" {
			bg = c.theme.Danger
		}
		return lipgloss.NewStyle().
			Bold(true).
			Foreground(c.theme.TextInverse).
			Background(bg).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(bg).
			Padding(0, 1).
			Render(text)
	}

	return lipgloss.NewStyle().
		Foreground(c.theme.Text).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.theme.Muted).
		Padding(0, 1).
		Render(text)
}
