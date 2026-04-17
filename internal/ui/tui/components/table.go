package components

import (
	"strings"

	btable "github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type TableSelectedMsg struct {
	Index int
	Row   btable.Row
}

type Table struct {
	theme   theme.Theme
	model   btable.Model
	columns []btable.Column
	rows    []btable.Row
}

func NewTable(th theme.Theme, columns []btable.Column, rows []btable.Row) Table {
	t := btable.New(btable.WithColumns(columns), btable.WithRows(rows), btable.WithFocused(true), btable.WithHeight(10))
	styles := btable.DefaultStyles()
	styles.Header = styles.Header.Foreground(th.TextInverse).Background(th.Primary).Bold(true)
	styles.Selected = styles.Selected.Foreground(th.Text).Background(th.SurfaceAlt).Bold(true)
	styles.Cell = styles.Cell.Foreground(th.Text)
	t.SetStyles(styles)
	table := Table{theme: th, model: t, columns: append([]btable.Column{}, columns...)}
	table.SetRows(rows)
	return table
}

func (t Table) Init() tea.Cmd { return nil }

func (t *Table) Resize(width, height int) {
	t.model.SetWidth(width)
	if height > 3 {
		t.model.SetHeight(height - 3)
	}
}

func (t Table) Update(msg tea.Msg) (Table, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		t.Resize(msg.Width, msg.Height)
	case tea.KeyMsg:
		if msg.String() == "enter" {
			row := t.model.SelectedRow()
			return t, func() tea.Msg {
				return TableSelectedMsg{Index: t.model.Cursor(), Row: row}
			}
		}
	}
	var cmd tea.Cmd
	t.model, cmd = t.model.Update(msg)
	return t, cmd
}

func (t *Table) SetRows(rows []btable.Row) {
	t.rows = append([]btable.Row{}, rows...)
	normalized := make([]btable.Row, 0, len(rows))
	for _, row := range rows {
		out := make(btable.Row, len(row))
		for i := range row {
			cell := row[i]
			if i < len(t.columns) && t.columns[i].Width > 0 {
				cell = truncateCell(cell, t.columns[i].Width-1)
			}
			out[i] = strings.ReplaceAll(cell, "\n", " ")
		}
		normalized = append(normalized, out)
	}
	t.model.SetRows(normalized)
}

func (t Table) SelectedRow() btable.Row { return t.model.SelectedRow() }

func (t Table) Cursor() int { return t.model.Cursor() }

func (t Table) View() string { return t.model.View() }

func truncateCell(value string, width int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}
