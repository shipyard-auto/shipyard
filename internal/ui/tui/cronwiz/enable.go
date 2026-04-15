package cronwiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type batchScreen struct {
	theme      theme.Theme
	service    CronService
	title      string
	breadcrumb []string
	enable     bool
	checklist  *components.Checklist
	empty      *components.Empty
	result     string
}

func newEnableScreen(th theme.Theme, service CronService) Screen {
	return newBatchScreen(th, service, true)
}

func newDisableScreen(th theme.Theme, service CronService) Screen {
	return newBatchScreen(th, service, false)
}

func newBatchScreen(th theme.Theme, service CronService, enable bool) Screen {
	s := &batchScreen{
		theme:      th,
		service:    service,
		enable:     enable,
		title:      map[bool]string{true: "Enable Jobs", false: "Disable Jobs"}[enable],
		breadcrumb: []string{"cron", map[bool]string{true: "enable", false: "disable"}[enable]},
	}
	s.refresh()
	return s
}

func (s *batchScreen) refresh() {
	jobs, _ := s.service.List()
	items := []components.ChecklistItem{}
	for _, job := range jobs {
		if s.enable && job.Enabled {
			continue
		}
		if !s.enable && !job.Enabled {
			continue
		}
		items = append(items, components.ChecklistItem{
			ID:       job.ID,
			Title:    fmt.Sprintf("%s  %s", job.ID, job.Name),
			Subtitle: job.Schedule,
		})
	}
	if len(items) == 0 {
		props := components.EmptyProps{Hint: "[esc] back"}
		if s.enable {
			props.Icon = "✓"
			props.Title = "All jobs are already enabled."
		} else {
			props.Icon = "○"
			props.Title = "No enabled jobs to disable."
		}
		empty := components.NewEmpty(s.theme, props)
		s.empty = &empty
		s.checklist = nil
		return
	}
	cl := components.NewChecklist(s.theme, items)
	s.checklist = &cl
	s.empty = nil
}

func (s *batchScreen) Init() tea.Cmd { return nil }
func (s *batchScreen) Title() string { return s.title }
func (s *batchScreen) Breadcrumb() []string { return s.breadcrumb }
func (s *batchScreen) Footer() []components.KeyHint {
	return []components.KeyHint{
		{Key: "space", Label: "toggle"},
		{Key: "a", Label: "select all"},
		{Key: "enter", Label: map[bool]string{true: "enable selected", false: "disable selected"}[s.enable]},
	}
}

func (s *batchScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		return newMenuScreen(s.theme, s.service), nil
	}
	if s.empty != nil {
		return s, nil
	}
	checklist, cmd := s.checklist.Update(msg)
	s.checklist = &checklist
	if cmd != nil {
		confirmed := cmd().(components.ChecklistConfirmedMsg)
		success := []string{}
		failures := []string{}
		for idx, id := range confirmed.SelectedIDs {
			if s.enable {
				if _, err := s.service.Enable(id); err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", id, err))
				} else {
					success = append(success, fmt.Sprintf("Enabled %d of %d: %s", idx+1, len(confirmed.SelectedIDs), id))
				}
			} else {
				if _, err := s.service.Disable(id); err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", id, err))
				} else {
					success = append(success, fmt.Sprintf("Disabled %d of %d: %s", idx+1, len(confirmed.SelectedIDs), id))
				}
			}
		}
		lines := []string{}
		if len(success) > 0 {
			lines = append(lines, s.theme.RenderSuccess(strings.Join(success, "\n")))
		}
		if len(failures) > 0 {
			lines = append(lines, s.theme.RenderError(strings.Join(failures, "\n")))
		}
		s.result = strings.Join(lines, "\n\n")
		s.refresh()
	}
	return s, nil
}

func (s *batchScreen) View() string {
	if s.empty != nil {
		return s.empty.View()
	}
	if s.result != "" {
		return s.checklist.View() + "\n\n" + s.result
	}
	return s.checklist.View()
}
