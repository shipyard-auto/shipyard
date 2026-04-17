package logwiz

import (
	"fmt"

	btable "github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type sourcesScreen struct {
	theme   theme.Theme
	service LogsService
	table   *components.Table
	empty   *components.Empty
	rows    []logs.SourceSummary
}

func newSourcesScreen(th theme.Theme, service LogsService) Screen {
	s := &sourcesScreen{theme: th, service: service}
	s.refresh()
	return s
}

func (s *sourcesScreen) refresh() {
	sources, _ := s.service.ListSources()
	s.rows = sources
	if len(sources) == 0 {
		empty := components.NewEmpty(s.theme, components.EmptyProps{
			Icon:        "⎋",
			Title:       "No logs yet.",
			Description: "Shipyard writes events automatically as subsystems run.",
			Hint:        "Use the cron wizard to create and run a job to produce log events.",
		})
		s.empty = &empty
		return
	}
	rows := make([]btable.Row, 0, len(sources))
	for _, source := range sources {
		rows = append(rows, btable.Row{
			source.Source,
			fmt.Sprintf("%d", source.Files),
			fmt.Sprintf("%d", source.SizeBytes),
			source.NewestFile,
		})
	}
	table := components.NewTable(s.theme,
		[]btable.Column{{Title: "Source", Width: 16}, {Title: "Files", Width: 8}, {Title: "Size", Width: 10}, {Title: "Newest file", Width: 20}},
		rows)
	s.table = &table
}

func (s *sourcesScreen) Init() tea.Cmd        { return nil }
func (s *sourcesScreen) Title() string        { return "View Sources" }
func (s *sourcesScreen) Breadcrumb() []string { return []string{"logs", "sources"} }
func (s *sourcesScreen) Footer() []components.KeyHint {
	return []components.KeyHint{{Key: "enter", Label: "inspect source"}}
}
func (s *sourcesScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		return newMenuScreen(s.theme, s.service), nil
	}
	if s.empty != nil {
		return s, nil
	}
	table, cmd := s.table.Update(msg)
	s.table = &table
	if cmd != nil {
		selected := cmd().(components.TableSelectedMsg)
		return newShowScreen(s.theme, s.service, s.rows[selected.Index].Source), nil
	}
	return s, nil
}
func (s *sourcesScreen) View() string {
	if s.empty != nil {
		return s.empty.View()
	}
	return s.table.View()
}
