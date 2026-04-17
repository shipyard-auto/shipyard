package cronwiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type batchStepMsg struct {
	id  string
	err error
}

type batchScreen struct {
	theme      theme.Theme
	service    CronService
	title      string
	breadcrumb []string
	enable     bool
	checklist  *components.Checklist
	empty      *components.Empty
	result     string
	successes  []string
	failures   []string
	pending    []string
	total      int
	index      int
	running    bool
	spinner    components.Spinner
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
		spinner:    components.NewSpinner(th, ""),
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

func (s *batchScreen) Init() tea.Cmd        { return nil }
func (s *batchScreen) Title() string        { return s.title }
func (s *batchScreen) Breadcrumb() []string { return s.breadcrumb }

func (s *batchScreen) Footer() []components.KeyHint {
	if s.running {
		return []components.KeyHint{{Key: "esc", Label: "cancel"}}
	}
	return []components.KeyHint{
		{Key: "space", Label: "toggle"},
		{Key: "a", Label: "select all"},
		{Key: "enter", Label: map[bool]string{true: "enable selected", false: "disable selected"}[s.enable]},
		{Key: "esc", Label: "cancel"},
	}
}

func (s *batchScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case batchStepMsg:
		if msg.id != "" {
			if msg.err != nil {
				s.failures = append(s.failures, fmt.Sprintf("%s: %v", msg.id, msg.err))
			} else {
				s.successes = append(s.successes, msg.id)
			}
			s.index++
		}
		if s.index >= len(s.pending) {
			s.running = false
			s.result = s.renderResult()
			s.refresh()
			return s, nil
		}
		s.spinner = components.NewSpinner(s.theme, s.progressLabel())
		return s, tea.Batch(s.spinner.Init(), s.executePending())
	case tea.KeyMsg:
		if msg.String() == "esc" {
			if s.running {
				s.running = false
				s.pending = nil
				s.result = s.theme.RenderError("Operation cancelled.")
				return s, nil
			}
			return newMenuScreen(s.theme, s.service), nil
		}
	}

	if s.running {
		spinner, cmd := s.spinner.Update(msg)
		s.spinner = spinner
		return s, cmd
	}
	if s.empty != nil {
		return s, nil
	}

	checklist, cmd := s.checklist.Update(msg)
	s.checklist = &checklist
	if cmd != nil {
		confirmed := cmd().(components.ChecklistConfirmedMsg)
		if len(confirmed.SelectedIDs) == 0 {
			s.result = s.theme.RenderError("No jobs selected.")
			return s, nil
		}
		s.pending = append([]string{}, confirmed.SelectedIDs...)
		s.total = len(s.pending)
		s.index = 0
		s.successes = nil
		s.failures = nil
		s.result = ""
		s.running = true
		s.spinner = components.NewSpinner(s.theme, s.progressLabel())
		return s, tea.Batch(s.spinner.Init(), s.executePending())
	}
	return s, nil
}

func (s *batchScreen) View() string {
	if s.empty != nil {
		return s.empty.View()
	}
	if s.running {
		return s.spinner.View()
	}
	if s.result != "" {
		return s.result
	}
	return s.checklist.View()
}

func (s *batchScreen) progressLabel() string {
	action := "Enabling"
	if !s.enable {
		action = "Disabling"
	}
	if s.total == 0 {
		return action + "…"
	}
	return fmt.Sprintf("%s %d of %d…", action, s.index+1, s.total)
}

func (s *batchScreen) executePending() tea.Cmd {
	if s.index >= len(s.pending) {
		return func() tea.Msg { return batchStepMsg{} }
	}
	id := s.pending[s.index]
	return func() tea.Msg {
		if s.enable {
			_, err := s.service.Enable(id)
			return batchStepMsg{id: id, err: err}
		}
		_, err := s.service.Disable(id)
		return batchStepMsg{id: id, err: err}
	}
}

func (s *batchScreen) renderResult() string {
	lines := []string{}
	if len(s.successes) > 0 {
		action := "Enabled"
		if !s.enable {
			action = "Disabled"
		}
		items := make([]string, 0, len(s.successes))
		for _, id := range s.successes {
			items = append(items, action+" "+id)
		}
		lines = append(lines, s.theme.RenderSuccess(strings.Join(items, "\n")))
	}
	if len(s.failures) > 0 {
		lines = append(lines, s.theme.RenderError(strings.Join(s.failures, "\n")))
	}
	return strings.Join(lines, "\n\n")
}
