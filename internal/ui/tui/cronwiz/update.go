package cronwiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type updateSubmitMsg struct {
	job cron.Job
	err error
}

type updateScreen struct {
	theme      theme.Theme
	service    CronService
	selectID   string
	picker     components.Menu
	target     *cron.Job
	add        *addScreen
	review     components.Confirm
	spinner    components.Spinner
	err        string
	updated    *cron.Job
	submitting bool
}

func newUpdateScreen(th theme.Theme, service CronService, preselectedID string) Screen {
	s := &updateScreen{
		theme:    th,
		service:  service,
		selectID: preselectedID,
		review:   components.NewConfirm(th, "Save these changes or go back to edit.", false),
		spinner:  components.NewSpinner(th, "Updating cron job…"),
	}
	s.bootstrap()
	return s
}

func (s *updateScreen) bootstrap() {
	if s.selectID != "" {
		if job, err := s.service.Get(s.selectID); err == nil {
			s.target = &job
			s.seed(job)
			return
		}
	}
	jobs, _ := s.service.List()
	s.picker = components.NewMenu(s.theme, jobsToMenuItems(jobs))
}

func (s *updateScreen) seed(job cron.Job) {
	screen := newAddScreen(s.theme, s.service, nil).(*addScreen)
	screen.name.SetValue(job.Name)
	screen.description.SetValue(job.Description)
	screen.command.SetValue(job.Command)
	screen.custom.SetValue(job.Schedule)
	screen.nameValue = job.Name
	screen.descValue = job.Description
	screen.schedule = job.Schedule
	screen.commandValue = job.Command
	screen.enabled = job.Enabled
	screen.step = 0
	s.add = screen
}

func (s *updateScreen) Init() tea.Cmd { return nil }
func (s *updateScreen) Title() string { return "Update Cron Job" }
func (s *updateScreen) Breadcrumb() []string { return []string{"cron", "update"} }

func (s *updateScreen) Footer() []components.KeyHint {
	switch {
	case s.target == nil:
		return []components.KeyHint{{Key: "↑↓", Label: "choose"}, {Key: "enter", Label: "select"}}
	case s.updated != nil:
		return []components.KeyHint{{Key: "enter", Label: "return to menu"}}
	case s.submitting:
		return []components.KeyHint{{Key: "esc", Label: "cancel"}}
	case s.add != nil && s.add.step == 5:
		if s.err != "" {
			return []components.KeyHint{{Key: "r", Label: "retry"}, {Key: "esc", Label: "cancel"}}
		}
		return []components.KeyHint{{Key: "←→", Label: "choose"}, {Key: "enter", Label: "select"}}
	default:
		return s.add.Footer()
	}
}

func (s *updateScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case updateSubmitMsg:
		s.submitting = false
		if msg.err != nil {
			s.err = msg.err.Error()
			return s, nil
		}
		s.updated = &msg.job
		s.err = ""
		return s, nil
	case tea.KeyMsg:
		if msg.String() == "esc" {
			switch {
			case s.updated != nil:
				return newMenuScreen(s.theme, s.service), nil
			case s.target == nil:
				return newMenuScreen(s.theme, s.service), nil
			case s.submitting:
				s.submitting = false
				s.err = "update cancelled"
				return s, nil
			case s.add != nil && s.add.step == 5:
				s.add.step = 4
				s.err = ""
				return s, nil
			}
		}
		if s.updated != nil && msg.String() == "enter" {
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.add != nil && s.add.step == 5 && s.err != "" && msg.String() == "r" {
			s.err = ""
			s.submitting = true
			return s, s.submit()
		}
	}

	if s.target == nil {
		jobs, _ := s.service.List()
		if len(jobs) == 0 {
			empty := components.NewEmpty(s.theme, components.EmptyProps{Icon: "⎋", Title: "No cron jobs to update.", Hint: "[esc] back"})
			if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
				return newMenuScreen(s.theme, s.service), nil
			}
			s.err = empty.View()
			return s, nil
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
			s.seed(job)
			return s, s.add.Init()
		}
		return s, nil
	}

	if s.updated != nil {
		return s, nil
	}

	if s.add.step < 5 {
		next, cmd := s.add.Update(msg)
		s.add = next.(*addScreen)
		return s, cmd
	}

	if s.submitting {
		spinner, cmd := s.spinner.Update(msg)
		s.spinner = spinner
		return s, cmd
	}

	review, cmd := s.review.Update(msg)
	s.review = review
	if cmd != nil {
		decision := cmd().(components.ConfirmMsg)
		if !decision.Accepted {
			s.add.step = 4
			return s, nil
		}
		s.err = ""
		s.submitting = true
		return s, s.submit()
	}
	return s, nil
}

func (s *updateScreen) View() string {
	if s.err != "" && s.target == nil && strings.Contains(s.err, "No cron jobs to update.") {
		return s.err
	}
	if s.target == nil {
		return strings.Join([]string{s.theme.LabelStyle.Render("Select a job to update"), s.picker.View()}, "\n\n")
	}
	if s.updated != nil {
		changed := s.changedFields(*s.updated)
		return s.theme.RenderSuccess("Cron job updated successfully.") + "\n\n" +
			renderReview(s.theme, "Updated job", [][2]string{
				{"Name", s.updated.Name},
				{"Description", blankOrValue(s.updated.Description)},
				{"Schedule", s.updated.Schedule},
				{"Command", s.updated.Command},
				{"Enabled", fmt.Sprintf("%t", s.updated.Enabled)},
			}, changed)
	}
	if s.add.step < 5 {
		if s.err != "" {
			return s.add.View() + "\n\n" + s.theme.RenderError(s.err)
		}
		return s.add.View()
	}

	lines := [][2]string{
		{"Name", s.add.nameValue},
		{"Description", blankOrValue(s.add.descValue)},
		{"Schedule", s.add.schedule},
		{"Summary", scheduleSummary(s.add.schedule)},
		{"Command", s.add.commandValue},
		{"Enabled", fmt.Sprintf("%t", s.add.enabled)},
	}
	changed := s.changedFields(cron.Job{
		ID:          s.target.ID,
		Name:        s.add.nameValue,
		Description: s.add.descValue,
		Schedule:    s.add.schedule,
		Command:     s.add.commandValue,
		Enabled:     s.add.enabled,
	})

	parts := []string{
		s.theme.SubtitleStyle.Render("Review changes"),
		renderReview(s.theme, "Update review", lines, changed),
	}
	if s.submitting {
		parts = append(parts, s.spinner.View())
	} else {
		parts = append(parts, s.review.View())
	}
	if s.err != "" {
		parts = append(parts, s.theme.RenderError(s.err))
	}
	return strings.Join(parts, "\n\n")
}

func (s *updateScreen) changedFields(job cron.Job) map[string]bool {
	return map[string]bool{
		"Name":        s.target.Name != job.Name,
		"Description": s.target.Description != job.Description,
		"Schedule":    s.target.Schedule != job.Schedule,
		"Summary":     s.target.Schedule != job.Schedule,
		"Command":     s.target.Command != job.Command,
		"Enabled":     s.target.Enabled != job.Enabled,
	}
}

func (s *updateScreen) submit() tea.Cmd {
	return func() tea.Msg {
		patch := cron.JobInput{}
		changed := false
		if s.target.Name != s.add.nameValue {
			patch.Name = strptr(s.add.nameValue)
			changed = true
		}
		if s.target.Description != s.add.descValue {
			patch.Description = strptr(s.add.descValue)
			changed = true
		}
		if s.target.Schedule != s.add.schedule {
			patch.Schedule = strptr(s.add.schedule)
			changed = true
		}
		if s.target.Command != s.add.commandValue {
			patch.Command = strptr(s.add.commandValue)
			changed = true
		}
		if s.target.Enabled != s.add.enabled {
			patch.Enabled = boolptr(s.add.enabled)
			changed = true
		}
		if !changed {
			return updateSubmitMsg{job: *s.target}
		}
		job, err := s.service.Update(s.target.ID, patch)
		return updateSubmitMsg{job: job, err: err}
	}
}
