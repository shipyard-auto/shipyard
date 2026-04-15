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
	theme    theme.Theme
	service  CronService
	selectID string
	picker   components.Menu
	target   *cron.Job
	add      *addScreen
	err      string
	updated  *cron.Job
}

func newUpdateScreen(th theme.Theme, service CronService, preselectedID string) Screen {
	s := &updateScreen{theme: th, service: service, selectID: preselectedID}
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
	if s.target == nil {
		return []components.KeyHint{{Key: "↑↓", Label: "choose"}, {Key: "enter", Label: "select"}}
	}
	if s.updated != nil {
		return []components.KeyHint{{Key: "enter", Label: "return to menu"}}
	}
	return s.add.Footer()
}

func (s *updateScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case updateSubmitMsg:
		if msg.err != nil {
			s.err = msg.err.Error()
			return s, nil
		}
		s.updated = &msg.job
		return s, nil
	case tea.KeyMsg:
		if msg.String() == "esc" {
			if s.updated != nil {
				return newMenuScreen(s.theme, s.service), nil
			}
			if s.target == nil {
				return newMenuScreen(s.theme, s.service), nil
			}
		}
		if s.updated != nil && msg.String() == "enter" {
			return newMenuScreen(s.theme, s.service), nil
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
		}
		return s, nil
	}

	if s.updated != nil {
		return s, nil
	}

	next, cmd := s.add.Update(msg)
	s.add = next.(*addScreen)
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" && s.add.step == 5 && s.add.created == nil {
		return s, func() tea.Msg {
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
	return s, cmd
}

func (s *updateScreen) View() string {
	if s.err != "" && s.target == nil && strings.Contains(s.err, "No cron jobs to update.") {
		return s.err
	}
	if s.target == nil {
		return strings.Join([]string{s.theme.LabelStyle.Render("Select a job to update"), s.picker.View()}, "\n\n")
	}
	if s.updated != nil {
		changed := map[string]bool{
			"Name":        s.target.Name != s.updated.Name,
			"Description": s.target.Description != s.updated.Description,
			"Schedule":    s.target.Schedule != s.updated.Schedule,
			"Command":     s.target.Command != s.updated.Command,
			"Enabled":     s.target.Enabled != s.updated.Enabled,
		}
		return s.theme.RenderSuccess("Cron job updated successfully.") + "\n\n" +
			renderReview(s.theme, "Updated job", [][2]string{
				{"Name", s.updated.Name},
				{"Description", blankOrValue(s.updated.Description)},
				{"Schedule", s.updated.Schedule},
				{"Command", s.updated.Command},
				{"Enabled", fmt.Sprintf("%t", s.updated.Enabled)},
			}, changed)
	}
	if s.err != "" {
		return s.add.View() + "\n\n" + s.theme.RenderError(s.err)
	}
	return s.add.View()
}
