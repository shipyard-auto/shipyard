package components

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type MenuItem struct {
	Title       string
	Description string
	Disabled    bool
	Badge       string
	Key         string
}

type MenuSelectedMsg struct {
	Index int
	Key   string
}

type Menu struct {
	theme theme.Theme
	items []MenuItem
	index int
	width int
}

func NewMenu(th theme.Theme, items []MenuItem) Menu {
	m := Menu{theme: th, items: append([]MenuItem{}, items...)}
	m.ensureSelectable()
	return m
}

func (m Menu) Init() tea.Cmd { return nil }

func (m Menu) Resize(width, _ int) { m.width = width }

func (m Menu) Update(msg tea.Msg) (Menu, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Resize(msg.Width, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "up":
			m.move(-1)
		case "down":
			m.move(1)
		case "enter":
			if len(m.items) == 0 || m.items[m.index].Disabled {
				return m, nil
			}
			selected := m.items[m.index]
			return m, func() tea.Msg {
				return MenuSelectedMsg{Index: m.index, Key: selected.Key}
			}
		}
	}
	return m, nil
}

func (m *Menu) move(delta int) {
	if len(m.items) == 0 {
		return
	}
	for i := 0; i < len(m.items); i++ {
		next := (m.index + delta + len(m.items)) % len(m.items)
		m.index = next
		if !m.items[m.index].Disabled {
			return
		}
	}
}

func (m *Menu) ensureSelectable() {
	for i, item := range m.items {
		if !item.Disabled {
			m.index = i
			return
		}
	}
	m.index = 0
}

func (m Menu) View() string {
	if len(m.items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(m.items))
	for i, item := range m.items {
		left := "  "
		style := m.theme.MenuItemStyle
		descStyle := m.theme.SubtitleStyle
		if i == m.index && !item.Disabled {
			left = theme.GlyphSelected + " "
			style = m.theme.MenuItemSelectedStyle
			descStyle = descStyle.Foreground(m.theme.Accent)
		}
		title := item.Title
		if item.Badge != "" {
			title += " [" + item.Badge + "]"
		}
		if item.Disabled {
			title = m.theme.SubtitleStyle.Render(title)
		} else {
			title = style.Render(title)
		}
		line := strings.Repeat(" ", theme.MenuIndent) + left + title
		if item.Description != "" {
			line += "\n" + strings.Repeat(" ", theme.MenuIndent+4) + descStyle.Render(item.Description)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
