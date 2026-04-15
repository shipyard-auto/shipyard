package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type MenuItem struct {
	Title       string
	Description string
	Disabled    bool
	Badge       string
	// BadgeVariant: "muted" (default), "accent", "success", "warning", "danger".
	// When empty, the menu auto-selects accent for the focused row, muted otherwise.
	BadgeVariant string
	Key          string
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

func (m Menu) SetWidth(width int) Menu {
	m.width = width
	return m
}

func (m Menu) Update(msg tea.Msg) (Menu, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.move(-1)
		case "down", "j":
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

func (m *Menu) SetSelectedByKey(key string) {
	for i, item := range m.items {
		if item.Key == key {
			m.index = i
			if item.Disabled {
				m.ensureSelectable()
			}
			return
		}
	}
}

func (m Menu) Selected() MenuItem {
	if len(m.items) == 0 {
		return MenuItem{}
	}
	return m.items[m.index]
}

// rowWidth returns the inner width available for a menu row.
func (m Menu) rowWidth() int {
	if m.width <= 0 {
		return 60
	}
	w := m.width - theme.MenuIndent*2
	if w < 30 {
		w = 30
	}
	return w
}

func (m Menu) View() string {
	if len(m.items) == 0 {
		return ""
	}

	rowWidth := m.rowWidth()
	indent := strings.Repeat(" ", theme.MenuIndent)
	descIndent := strings.Repeat(" ", theme.MenuIndent+3)

	rows := make([]string, 0, len(m.items)*2)

	for i, item := range m.items {
		isSelected := i == m.index && !item.Disabled

		// Selection indicator (left edge).
		marker := "  "
		if isSelected {
			marker = lipgloss.NewStyle().
				Foreground(m.theme.Accent).
				Bold(true).
				Render(theme.GlyphSelected) + " "
		}

		// Title styling.
		titleStyle := m.theme.MenuItemStyle
		if item.Disabled {
			titleStyle = m.theme.SubtitleStyle.Italic(false)
		} else if isSelected {
			titleStyle = m.theme.MenuItemSelectedStyle
		}
		title := titleStyle.Render(item.Title)

		// Badge as pill (single line, no border).
		badge := ""
		if item.Badge != "" {
			variant := item.BadgeVariant
			if variant == "" {
				if item.Disabled {
					variant = "muted"
				} else if isSelected {
					variant = "accent"
				} else {
					variant = "muted"
				}
			}
			badge = m.theme.Pill(item.Badge, variant)
		}

		// Compose left + right with right alignment.
		leftBlock := marker + title
		var titleRow string
		if badge != "" {
			gap := rowWidth - lipgloss.Width(leftBlock) - lipgloss.Width(badge)
			if gap < 2 {
				gap = 2
			}
			titleRow = leftBlock + strings.Repeat(" ", gap) + badge
		} else {
			titleRow = leftBlock
		}
		titleRow = indent + titleRow

		rows = append(rows, titleRow)

		if item.Description != "" {
			descStyle := m.theme.SubtitleStyle
			if isSelected {
				descStyle = lipgloss.NewStyle().Foreground(m.theme.Muted)
			}
			rows = append(rows, descIndent+descStyle.Render(item.Description))
		}

		// Subtle spacing between items.
		if i < len(m.items)-1 {
			rows = append(rows, "")
		}
	}

	return strings.Join(rows, "\n")
}

func (m Menu) DebugString() string {
	return fmt.Sprintf("items=%d index=%d width=%d", len(m.items), m.index, m.width)
}
