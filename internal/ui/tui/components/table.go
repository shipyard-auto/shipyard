package components

import (
	tea "github.com/charmbracelet/bubbletea"
	btable "github.com/charmbracelet/bubbles/table"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type TableSelectedMsg struct {
	Index int
	Row   btable.Row
}

type Table struct {
	theme theme.Theme
	model btable.Model
}

func NewTable(th theme.Theme, columns []btable.Column, rows []btable.Row) Table {
	t := btable.New(btable.WithColumns(columns), btable.WithRows(rows), btable.WithFocused(true), btable.WithHeight(10))
	styles := btable.DefaultStyles()
	styles.Header = styles.Header.Foreground(th.TextInverse).Background(th.Primary).Bold(true)
	styles.Selected = styles.Selected.Foreground(th.Text).Background(th.SurfaceAlt).Bold(true)
	t.SetStyles(styles)
	return Table{theme: th, model: t}
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

func (t *Table) SetRows(rows []btable.Row) { t.model.SetRows(rows) }

func (t Table) SelectedRow() btable.Row { return t.model.SelectedRow() }

func (t Table) Cursor() int { return t.model.Cursor() }

func (t Table) View() string { return t.model.View() }
