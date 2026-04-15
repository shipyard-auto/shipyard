package cronwiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type runResultMsg struct {
	job    cron.Job
	output string
	err    error
}

type runScreen struct {
	theme    theme.Theme
	service  CronService
	selectID string
	picker   components.Menu
	target   *cron.Job
	confirm  *components.Confirm
	output   string
	err      string
	done     bool
}

func newRunScreen(th theme.Theme, service CronService, preselectedID string) Screen {
	s := &runScreen{theme: th, service: service, selectID: preselectedID}
	s.bootstrap()
	return s
}

func (s *runScreen) bootstrap() {
	if s.selectID != "" {
		if job, err := s.service.Get(s.selectID); err == nil {
			s.target = &job
			confirm := components.NewConfirm(s.theme, fmt.Sprintf("Run %s now?", job.ID), false)
			s.confirm = &confirm
			return
		}
	}
	jobs, _ := s.service.List()
	s.picker = components.NewMenu(s.theme, jobsToMenuItems(jobs))
}

func (s *runScreen) Init() tea.Cmd { return nil }
func (s *runScreen) Title() string { return "Run Job Now" }
func (s *runScreen) Breadcrumb() []string { return []string{"cron", "run"} }
func (s *runScreen) Footer() []components.KeyHint {
	if s.done {
		return []components.KeyHint{{Key: "enter", Label: "menu"}}
	}
	if s.target == nil {
		return []components.KeyHint{{Key: "↑↓", Label: "choose"}, {Key: "enter", Label: "select"}}
	}
	return []components.KeyHint{{Key: "enter", Label: "confirm"}}
}

func (s *runScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case runResultMsg:
		s.done = true
		s.err = ""
		s.output = msg.output
		if msg.err != nil {
			s.err = msg.err.Error()
		}
		return s, nil
	case tea.KeyMsg:
		if msg.String() == "esc" {
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.done && msg.String() == "enter" {
			return newMenuScreen(s.theme, s.service), nil
		}
	}
	if s.target == nil {
		jobs, _ := s.service.List()
		if len(jobs) == 0 {
			empty := components.NewEmpty(s.theme, components.EmptyProps{Icon: "⎋", Title: "No cron jobs available to run.", Hint: "[esc] back"})
			return &staticScreen{theme: s.theme, title: s.Title(), crumb: s.Breadcrumb(), footer: s.Footer(), body: empty.View(), next: newMenuScreen(s.theme, s.service)}, nil
		}
		menu, cmd := s.picker.Update(msg)
		s.picker = menu
		if cmd != nil {
			selected := cmd().(components.MenuSelectedMsg)
			job, err := s.service.Get(selected.Key)
			if err != nil {
				s.err = err.Error()
				return s, nil
			}
			s.target = &job
			confirm := components.NewConfirm(s.theme, fmt.Sprintf("Run %s — %s now?", job.ID, job.Name), false)
			s.confirm = &confirm
		}
		return s, nil
	}
	confirm, cmd := s.confirm.Update(msg)
	s.confirm = &confirm
	if cmd != nil {
		decision := cmd().(components.ConfirmMsg)
		if !decision.Accepted {
			return newMenuScreen(s.theme, s.service), nil
		}
		return s, func() tea.Msg {
			job, output, err := s.service.Run(s.target.ID)
			return runResultMsg{job: job, output: output, err: err}
		}
	}
	return s, nil
}

func (s *runScreen) View() string {
	if s.target == nil {
		return s.picker.View()
	}
	if s.done {
		body := []string{}
		if s.err != "" {
			body = append(body, s.theme.RenderError(s.err))
		} else {
			body = append(body, s.theme.RenderSuccess("Run finished successfully."))
		}
		viewer := components.NewViewer(s.theme, strings.TrimSpace(s.output))
		body = append(body, "", viewer.View())
		return strings.Join(body, "\n")
	}
	return s.confirm.View()
}
