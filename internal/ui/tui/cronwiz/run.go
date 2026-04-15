package cronwiz

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type runResultMsg struct {
	job      cron.Job
	output   string
	err      error
	elapsed  time.Duration
	finished bool
}

type runScreen struct {
	theme     theme.Theme
	service   CronService
	selectID  string
	picker    components.Menu
	target    *cron.Job
	confirm   *components.Confirm
	viewer    components.Viewer
	spinner   components.Spinner
	output    string
	err       string
	done      bool
	running   bool
	startedAt time.Time
	elapsed   time.Duration
}

func newRunScreen(th theme.Theme, service CronService, preselectedID string) Screen {
	s := &runScreen{
		theme:    th,
		service:  service,
		selectID: preselectedID,
		viewer:   components.NewViewer(th, ""),
		spinner:  components.NewSpinner(th, "Running…"),
	}
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
	switch {
	case s.done:
		return []components.KeyHint{{Key: "enter", Label: "menu"}}
	case s.running:
		return []components.KeyHint{{Key: "esc", Label: "cancel"}}
	case s.target == nil:
		return []components.KeyHint{{Key: "↑↓", Label: "choose"}, {Key: "enter", Label: "select"}}
	default:
		return []components.KeyHint{{Key: "←→", Label: "choose"}, {Key: "enter", Label: "select"}}
	}
}

func (s *runScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case runResultMsg:
		s.running = false
		s.done = true
		s.err = ""
		s.output = strings.TrimSpace(msg.output)
		s.elapsed = msg.elapsed
		if msg.err != nil {
			s.err = msg.err.Error()
		}
		s.viewer.SetContent(s.output)
		return s, nil
	case tea.KeyMsg:
		if msg.String() == "esc" {
			if s.running {
				s.running = false
				s.err = "run cancelled"
				s.done = true
				return s, nil
			}
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
	if s.running {
		spinner, cmd := s.spinner.Update(msg)
		s.spinner = spinner
		return s, cmd
	}
	if s.done {
		viewer, cmd := s.viewer.Update(msg)
		s.viewer = viewer
		return s, cmd
	}
	confirm, cmd := s.confirm.Update(msg)
	s.confirm = &confirm
	if cmd != nil {
		decision := cmd().(components.ConfirmMsg)
		if !decision.Accepted {
			return newMenuScreen(s.theme, s.service), nil
		}
		s.running = true
		s.startedAt = time.Now()
		return s, tea.Batch(s.spinner.Init(), func() tea.Msg {
			job, output, err := s.service.Run(s.target.ID)
			return runResultMsg{
				job:      job,
				output:   output,
				err:      err,
				elapsed:  time.Since(s.startedAt),
				finished: true,
			}
		})
	}
	return s, nil
}

func (s *runScreen) View() string {
	if s.target == nil {
		return s.picker.View()
	}
	if s.running {
		return strings.Join([]string{
			s.spinner.View(),
			"",
			s.viewer.View(),
		}, "\n")
	}
	if s.done {
		lines := []string{}
		if s.err != "" {
			lines = append(lines, s.theme.RenderError("Run failed: "+s.err))
		} else {
			lines = append(lines, s.theme.RenderSuccess("Run finished successfully."))
		}
		lines = append(lines, s.theme.RenderHint("Elapsed time: "+s.elapsed.Round(time.Millisecond).String()))
		if strings.TrimSpace(s.output) != "" {
			lines = append(lines, "", s.viewer.View())
		}
		return strings.Join(lines, "\n")
	}
	return strings.Join([]string{
		renderReview(s.theme, "Run review", [][2]string{
			{"ID", s.target.ID},
			{"Name", s.target.Name},
			{"Schedule", s.target.Schedule},
			{"Command", s.target.Command},
		}, nil),
		"",
		s.confirm.View(),
	}, "\n")
}
