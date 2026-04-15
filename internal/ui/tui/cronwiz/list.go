package cronwiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	btable "github.com/charmbracelet/bubbles/table"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type listScreen struct {
	theme   theme.Theme
	service CronService
	table   *components.Table
	empty   *components.Empty
	jobs    []cron.Job
	detail  string
	spinner *components.Spinner
}

func newListScreen(th theme.Theme, service CronService) Screen {
	s := &listScreen{theme: th, service: service}
	s.refresh()
	return s
}

func (s *listScreen) refresh() {
	jobs, _ := s.service.List()
	s.jobs = jobs
	if len(jobs) == 0 {
		empty := components.NewEmpty(s.theme, components.EmptyProps{
			Icon:        "⎋",
			Title:       "No cron jobs to browse.",
			Description: "Add one from the main menu.",
			Hint:        "[esc] back",
		})
		s.empty = &empty
		s.table = nil
		return
	}
	rows := make([]btable.Row, 0, len(jobs))
	for _, job := range jobs {
		rows = append(rows, btable.Row{
			job.ID,
			job.Name,
			job.Schedule,
			fmt.Sprintf("%t", job.Enabled),
			job.Command,
		})
	}
	tb := components.NewTable(s.theme,
		[]btable.Column{
			{Title: "ID", Width: 8},
			{Title: "Name", Width: 18},
			{Title: "Schedule", Width: 14},
			{Title: "Enabled", Width: 9},
			{Title: "Command", Width: 40},
		},
		rows,
	)
	s.table = &tb
	s.empty = nil
}

func (s *listScreen) Init() tea.Cmd { return nil }
func (s *listScreen) Title() string { return "Browse Jobs" }
func (s *listScreen) Breadcrumb() []string { return []string{"cron", "browse"} }
func (s *listScreen) Footer() []components.KeyHint {
	return []components.KeyHint{
		{Key: "enter", Label: "details"},
		{Key: "e", Label: "edit"},
		{Key: "r", Label: "run"},
		{Key: "d", Label: "delete"},
		{Key: "space", Label: "toggle"},
	}
}

func (s *listScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			return newMenuScreen(s.theme, s.service), nil
		case "e":
			if len(s.jobs) > 0 {
				return newUpdateScreen(s.theme, s.service, s.jobs[s.table.Cursor()].ID), nil
			}
		case "r":
			if len(s.jobs) > 0 {
				return newRunScreen(s.theme, s.service, s.jobs[s.table.Cursor()].ID), nil
			}
		case "d":
			if len(s.jobs) > 0 {
				return newDeleteScreen(s.theme, s.service, s.jobs[s.table.Cursor()].ID), nil
			}
		case " ":
			if len(s.jobs) > 0 {
				job := s.jobs[s.table.Cursor()]
				label := "Disabling selected job…"
				if job.Enabled {
					sp := components.NewSpinner(s.theme, label)
					s.spinner = &sp
					_, _ = s.service.Disable(job.ID)
				} else {
					label = "Enabling selected job…"
					sp := components.NewSpinner(s.theme, label)
					s.spinner = &sp
					_, _ = s.service.Enable(job.ID)
				}
				s.refresh()
				s.spinner = nil
			}
		}
	}
	if s.empty != nil {
		return s, nil
	}
	table, cmd := s.table.Update(msg)
	s.table = &table
	if cmd != nil {
		if selected, ok := cmd().(components.TableSelectedMsg); ok {
			job := s.jobs[selected.Index]
			s.detail = renderReview(s.theme, "Job details", [][2]string{
				{"ID", job.ID},
				{"Name", job.Name},
				{"Schedule", job.Schedule},
				{"Command", job.Command},
				{"Enabled", fmt.Sprintf("%t", job.Enabled)},
			}, nil)
		}
	}
	return s, nil
}

func (s *listScreen) View() string {
	if s.empty != nil {
		return s.empty.View()
	}
	table := s.table.View()
	if s.spinner != nil {
		table += "\n\n" + s.spinner.View()
	}
	if s.detail == "" {
		return table
	}
	return strings.Join([]string{table, "", s.detail}, "\n")
}
