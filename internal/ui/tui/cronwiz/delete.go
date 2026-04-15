package cronwiz

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type deleteScreen struct {
	theme    theme.Theme
	service  CronService
	selectID string
	picker   components.Menu
	target   *cron.Job
	confirm  *components.Confirm
	done     bool
	err      string
}

func newDeleteScreen(th theme.Theme, service CronService, preselectedID string) Screen {
	s := &deleteScreen{theme: th, service: service, selectID: preselectedID}
	s.bootstrap()
	return s
}

func (s *deleteScreen) bootstrap() {
	if s.selectID != "" {
		if job, err := s.service.Get(s.selectID); err == nil {
			s.target = &job
			confirm := components.NewConfirm(s.theme, fmt.Sprintf("Delete %s — %s? This cannot be undone.", job.ID, job.Name), true)
			s.confirm = &confirm
			return
		}
	}
	jobs, _ := s.service.List()
	s.picker = components.NewMenu(s.theme, jobsToMenuItems(jobs))
}

func (s *deleteScreen) Init() tea.Cmd { return nil }
func (s *deleteScreen) Title() string { return "Delete Cron Job" }
func (s *deleteScreen) Breadcrumb() []string { return []string{"cron", "delete"} }
func (s *deleteScreen) Footer() []components.KeyHint {
	if s.done {
		return []components.KeyHint{{Key: "enter", Label: "menu"}}
	}
	if s.target == nil {
		return []components.KeyHint{{Key: "↑↓", Label: "choose"}, {Key: "enter", Label: "select"}, {Key: "esc", Label: "back"}}
	}
	return []components.KeyHint{{Key: "←→", Label: "choose"}, {Key: "enter", Label: "select"}, {Key: "esc", Label: "back"}}
}

func (s *deleteScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == "esc" {
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.done && key.String() == "enter" {
			return newMenuScreen(s.theme, s.service), nil
		}
	}
	if s.target == nil {
		jobs, _ := s.service.List()
		if len(jobs) == 0 {
			empty := components.NewEmpty(s.theme, components.EmptyProps{Icon: "⎋", Title: "No cron jobs to delete.", Hint: "[esc] back"})
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
			confirm := components.NewConfirm(s.theme, fmt.Sprintf("Delete %s — %s? This cannot be undone.", job.ID, job.Name), true)
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
		if err := s.service.Delete(s.target.ID); err != nil {
			s.err = err.Error()
		} else {
			s.done = true
		}
	}
	return s, nil
}

func (s *deleteScreen) View() string {
	if s.done {
		return s.theme.RenderSuccess("Cron job deleted successfully.") + "\n\n" +
			renderReview(s.theme, "Deleted job", [][2]string{
				{"ID", s.target.ID},
				{"Name", s.target.Name},
				{"Schedule", s.target.Schedule},
			}, nil)
	}
	if s.err != "" {
		return s.theme.RenderError(s.err)
	}
	if s.target == nil {
		return s.picker.View()
	}
	return s.confirm.View()
}

type staticScreen struct {
	theme  theme.Theme
	title  string
	crumb  []string
	footer []components.KeyHint
	body   string
	next   Screen
}

func (s *staticScreen) Init() tea.Cmd                             { return nil }
func (s *staticScreen) Title() string                             { return s.title }
func (s *staticScreen) Breadcrumb() []string                      { return s.crumb }
func (s *staticScreen) Footer() []components.KeyHint              { return s.footer }
func (s *staticScreen) View() string                              { return s.body }
func (s *staticScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		return s.next, nil
	}
	return s, nil
}
