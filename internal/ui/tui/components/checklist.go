package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type ChecklistItem struct {
	ID       string
	Title    string
	Subtitle string
	Checked  bool
}

type ChecklistConfirmedMsg struct {
	SelectedIDs []string
}

type Checklist struct {
	theme theme.Theme
	items []ChecklistItem
	index int
	width int
}

func NewChecklist(th theme.Theme, items []ChecklistItem) Checklist {
	return Checklist{theme: th, items: append([]ChecklistItem{}, items...)}
}

func (c Checklist) Init() tea.Cmd { return nil }

func (c *Checklist) Resize(width, _ int) { c.width = width }

func (c Checklist) Update(msg tea.Msg) (Checklist, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.Resize(msg.Width, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "up":
			if c.index > 0 {
				c.index--
			}
		case "down":
			if c.index < len(c.items)-1 {
				c.index++
			}
		case " ":
			if len(c.items) > 0 {
				c.items[c.index].Checked = !c.items[c.index].Checked
			}
		case "a":
			allChecked := true
			for _, item := range c.items {
				if !item.Checked {
					allChecked = false
					break
				}
			}
			for i := range c.items {
				c.items[i].Checked = !allChecked
			}
		case "enter":
			selected := make([]string, 0, len(c.items))
			for _, item := range c.items {
				if item.Checked {
					selected = append(selected, item.ID)
				}
			}
			return c, func() tea.Msg { return ChecklistConfirmedMsg{SelectedIDs: selected} }
		}
	}
	return c, nil
}

func (c Checklist) View() string {
	lines := make([]string, 0, len(c.items))
	for i, item := range c.items {
		cursor := "  "
		if i == c.index {
			cursor = theme.GlyphSelected + " "
		}
		box := c.theme.CheckboxUnchecked()
		if item.Checked {
			box = c.theme.CheckboxChecked()
		}
		line := fmt.Sprintf("%s%s%s", cursor, box, item.Title)
		if item.Subtitle != "" {
			line += "\n    " + c.theme.SubtitleStyle.Render(item.Subtitle)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
