package cronwiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type listScreen struct {
	theme   theme.Theme
	service CronService
	empty   *components.Empty
	jobs    []cron.Job
	detail  string
	cursor  int
	width   int
}

func newListScreen(th theme.Theme, service CronService) Screen {
	s := &listScreen{theme: th, service: service}
	s.refresh()
	return s
}

func (s *listScreen) refresh() {
	jobs, _ := s.service.List()
	s.jobs = jobs
	if s.cursor >= len(jobs) && len(jobs) > 0 {
		s.cursor = len(jobs) - 1
	}
	if len(jobs) == 0 {
		s.cursor = 0
	}
	if len(jobs) == 0 {
		empty := components.NewEmpty(s.theme, components.EmptyProps{
			Icon:        "⎋",
			Title:       "No cron jobs to browse.",
			Description: "Add one from the main menu.",
			Hint:        "[esc] back",
		})
		s.empty = &empty
		return
	}
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
		case "up":
			if s.cursor > 0 {
				s.cursor--
			}
		case "down":
			if s.cursor < len(s.jobs)-1 {
				s.cursor++
			}
		case "enter":
			if len(s.jobs) > 0 {
				job := s.jobs[s.cursor]
				s.detail = renderReview(s.theme, "Job details", [][2]string{
					{"ID", job.ID},
					{"Name", job.Name},
					{"Schedule", job.Schedule},
					{"Command", job.Command},
					{"Enabled", fmt.Sprintf("%t", job.Enabled)},
				}, nil)
			}
		case "e":
			if len(s.jobs) > 0 {
				return newUpdateScreen(s.theme, s.service, s.jobs[s.cursor].ID), nil
			}
		case "r":
			if len(s.jobs) > 0 {
				return newRunScreen(s.theme, s.service, s.jobs[s.cursor].ID), nil
			}
		case "d":
			if len(s.jobs) > 0 {
				return newDeleteScreen(s.theme, s.service, s.jobs[s.cursor].ID), nil
			}
		case " ":
			if len(s.jobs) > 0 {
				job := s.jobs[s.cursor]
				if job.Enabled {
					_, _ = s.service.Disable(job.ID)
				} else {
					_, _ = s.service.Enable(job.ID)
				}
				s.refresh()
			}
		}
		return s, nil
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		s.width = size.Width
	}
	return s, nil
}

func (s *listScreen) View() string {
	if s.empty != nil {
		return s.empty.View()
	}
	table := s.renderTable()
	if s.detail == "" {
		return table
	}
	return strings.Join([]string{table, "", s.detail}, "\n")
}

func (s *listScreen) renderTable() string {
	totalWidth := s.width
	if totalWidth <= 0 {
		totalWidth = 100
	}
	contentWidth := s.theme.ContentWidth(totalWidth) - 4
	if contentWidth < 72 {
		contentWidth = 72
	}

	idW := 8
	nameW := 18
	scheduleW := 14
	enabledW := 9
	commandW := contentWidth - idW - nameW - scheduleW - enabledW - 12
	if commandW < 18 {
		commandW = 18
	}

	header := lipgloss.JoinHorizontal(lipgloss.Left,
		s.headerCell("ID", idW),
		s.headerCell("Name", nameW),
		s.headerCell("Schedule", scheduleW),
		s.headerCell("Enabled", enabledW),
		s.headerCell("Command", commandW),
	)

	lines := []string{header}
	for i, job := range s.jobs {
		line := lipgloss.JoinHorizontal(lipgloss.Left,
			s.cell(job.ID, idW, i == s.cursor),
			s.cell(job.Name, nameW, i == s.cursor),
			s.cell(job.Schedule, scheduleW, i == s.cursor),
			s.cell(fmt.Sprintf("%t", job.Enabled), enabledW, i == s.cursor),
			s.cell(job.Command, commandW, i == s.cursor),
		)
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (s *listScreen) headerCell(value string, width int) string {
	style := lipgloss.NewStyle().
		Width(width).
		Padding(0, 1).
		Bold(true).
		Foreground(s.theme.TextInverse).
		Background(s.theme.Primary)
	return style.Render(truncateCell(value, width-2))
}

func (s *listScreen) cell(value string, width int, selected bool) string {
	style := lipgloss.NewStyle().Width(width).Padding(0, 1)
	if selected {
		style = style.Bold(true).Foreground(s.theme.Text).Background(s.theme.SurfaceAlt)
	}
	return style.Render(truncateCell(value, width-2))
}

func truncateCell(value string, width int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.TrimSpace(value)
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
