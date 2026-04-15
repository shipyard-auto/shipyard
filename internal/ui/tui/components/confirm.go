package components

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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
		case "left", "right", "tab":
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
	confirm := " Confirm "
	cancel := " Cancel "
	if c.focus == 0 {
		confirm = c.theme.InputFocusedStyle.Render(confirm)
		cancel = c.theme.InputStyle.Render(cancel)
	} else {
		confirm = c.theme.InputStyle.Render(confirm)
		cancel = c.theme.InputFocusedStyle.Render(cancel)
	}
	return strings.Join([]string{
		c.prompt,
		"",
		confirm + "  " + cancel,
	}, "\n")
}
