package servicewiz

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	btable "github.com/charmbracelet/bubbles/table"

	svcpkg "github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type listScreen struct {
	theme   theme.Theme
	service ServiceAPI
	table   *components.Table
	empty   *components.Empty
	records []svcpkg.ServiceRecord
	states  map[string]svcpkg.RuntimeStatus
	detail  string
}

func newListScreen(th theme.Theme, service ServiceAPI) Screen {
	s := &listScreen{theme: th, service: service}
	s.refresh()
	return s
}

func (s *listScreen) refresh() {
	records, _ := s.service.List()
	s.records = records
	s.states = map[string]svcpkg.RuntimeStatus{}
	if len(records) == 0 {
		empty := components.NewEmpty(s.theme, components.EmptyProps{
			Icon:        "⎋",
			Title:       "No services to browse.",
			Description: "Add one from the main menu.",
			Hint:        "[esc] back",
		})
		s.empty = &empty
		s.table = nil
		return
	}
	rows := make([]btable.Row, 0, len(records))
	for _, record := range records {
		_, status, _ := s.service.Status(record.ID)
		s.states[record.ID] = status
		rows = append(rows, btable.Row{
			record.ID,
			record.Name,
			fallback(status.State, "unknown"),
			boolLabel(record.Enabled),
			record.Command,
		})
	}
	tb := components.NewTable(s.theme, []btable.Column{
		{Title: "ID", Width: 8},
		{Title: "Name", Width: 18},
		{Title: "State", Width: 12},
		{Title: "Enabled", Width: 10},
		{Title: "Command", Width: 40},
	}, rows)
	s.table = &tb
	s.empty = nil
}

func (s *listScreen) Init() tea.Cmd { return nil }
func (s *listScreen) Title() string { return "Browse Services" }
func (s *listScreen) Breadcrumb() []string { return []string{"service", "browse"} }
func (s *listScreen) Footer() []components.KeyHint {
	return []components.KeyHint{{Key: "enter", Label: "details"}, {Key: "u", Label: "update"}, {Key: "x", Label: "status"}, {Key: "d", Label: "delete"}, {Key: "esc", Label: "back"}}
}

func (s *listScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			return newMenuScreen(s.theme, s.service), nil
		case "u":
			if len(s.records) > 0 {
				return newUpdateScreen(s.theme, s.service, s.records[s.table.Cursor()].ID), nil
			}
		case "x":
			if len(s.records) > 0 {
				return newStatusScreen(s.theme, s.service, s.records[s.table.Cursor()].ID), nil
			}
		case "d":
			if len(s.records) > 0 {
				return newDeleteScreen(s.theme, s.service, s.records[s.table.Cursor()].ID), nil
			}
		}
	}
	if s.empty != nil {
		return s, nil
	}
	table, cmd := s.table.Update(msg)
	s.table = &table
	if cmd != nil {
		selected := cmd().(components.TableSelectedMsg)
		record := s.records[selected.Index]
		status := s.states[record.ID]
		s.detail = renderReview(s.theme, "Service details", [][2]string{
			{"ID", record.ID},
			{"Name", record.Name},
			{"State", fallback(status.State, "unknown")},
			{"Enabled", boolLabel(record.Enabled)},
			{"Command", record.Command},
		}, nil)
	}
	return s, nil
}

func (s *listScreen) View() string {
	if s.empty != nil {
		return s.empty.View()
	}
	table := s.table.View()
	if s.detail == "" {
		return table
	}
	return strings.Join([]string{table, "", s.detail}, "\n")
}
