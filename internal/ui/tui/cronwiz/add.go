package cronwiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type addSubmitMsg struct {
	job cron.Job
	err error
}

type addScreen struct {
	theme        theme.Theme
	service      CronService
	name         components.Input
	description  components.Input
	custom       components.Input
	command      components.Input
	presetMenu   components.Menu
	enableMenu   components.Menu
	step         int
	useCustom    bool
	nameValue    string
	descValue    string
	schedule     string
	commandValue string
	enabled      bool
	err          string
	created      *cron.Job
}

func newAddScreen(th theme.Theme, service CronService, preset *cron.Job) Screen {
	name := components.NewInput(th, "Job name", "Backup Home", func(v string) error {
		return validateSingleLineRequired("name", v)
	})
	description := components.NewInput(th, "Description (optional)", "Optional context", func(v string) error {
		return validateOptionalSingleLine("description", v)
	})
	custom := components.NewInput(th, "Custom schedule", "* * * * *", validateSchedule)
	command := components.NewInput(th, "Command", "/bin/sh -lc 'echo hello'", func(v string) error {
		return validateSingleLineRequired("command", v)
	})
	name.SetHint("A short human-readable label for this job.")
	description.SetHint("Press Enter to skip.")
	custom.SetHint("Enter a 5-field cron expression.")
	command.SetHint("The shell command to run. No multi-line values.")

	presets := make([]components.MenuItem, 0, len(schedulePresets)+1)
	for _, preset := range schedulePresets {
		presets = append(presets, components.MenuItem{Title: preset.Title, Description: preset.Expression, Key: preset.Expression})
	}
	presets = append(presets, components.MenuItem{Title: "Custom expression…", Description: "Type a custom schedule", Key: "custom"})

	enable := components.NewMenu(th, []components.MenuItem{
		{Title: "Yes", Description: "Create the job enabled", Key: "yes"},
		{Title: "No", Description: "Save the job disabled", Key: "no"},
	})

	return &addScreen{
		theme:      th,
		service:    service,
		name:       name,
		description: description,
		custom:     custom,
		command:    command,
		presetMenu: components.NewMenu(th, presets),
		enableMenu: enable,
		enabled:    true,
	}
}

func (s *addScreen) Init() tea.Cmd { return s.name.Init() }

func (s *addScreen) Title() string { return "Add Cron Job" }

func (s *addScreen) Breadcrumb() []string { return []string{"cron", "add"} }

func (s *addScreen) Footer() []components.KeyHint {
	switch {
	case s.created != nil:
		return []components.KeyHint{{Key: "enter", Label: "return to menu"}}
	case s.step == 4:
		return []components.KeyHint{{Key: "↑↓", Label: "choose"}, {Key: "enter", Label: "select"}}
	case s.step == 5:
		return []components.KeyHint{{Key: "enter", Label: "confirm"}, {Key: "esc", Label: "edit"}}
	default:
		return []components.KeyHint{{Key: "enter", Label: "next"}}
	}
}

func (s *addScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case addSubmitMsg:
		if msg.err != nil {
			s.err = msg.err.Error()
			return s, nil
		}
		s.created = &msg.job
		s.err = ""
		return s, nil
	case tea.KeyMsg:
		if msg.String() == "esc" {
			if s.created != nil {
				return newMenuScreen(s.theme, s.service), nil
			}
			if s.step == 5 {
				s.step = 0
				return s, nil
			}
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.created != nil && msg.String() == "enter" {
			return newMenuScreen(s.theme, s.service), nil
		}
	}

	switch s.step {
	case 0:
		cmd, submitted := s.name.Update(msg)
		if submitted {
			s.nameValue = s.name.Value()
			s.step = 1
			return s, s.description.Init()
		}
		return s, cmd
	case 1:
		cmd, submitted := s.description.Update(msg)
		if submitted {
			s.descValue = s.description.Value()
			s.step = 2
			return s, nil
		}
		return s, cmd
	case 2:
		menu, cmd := s.presetMenu.Update(msg)
		s.presetMenu = menu
		if cmd != nil {
			selected := cmd().(components.MenuSelectedMsg)
			if selected.Key == "custom" {
				s.useCustom = true
				s.step = 21
				return s, s.custom.Init()
			}
			s.schedule = selected.Key
			s.step = 3
			return s, s.command.Init()
		}
		return s, nil
	case 21:
		cmd, submitted := s.custom.Update(msg)
		if submitted {
			s.schedule = s.custom.Value()
			s.step = 3
			return s, s.command.Init()
		}
		return s, cmd
	case 3:
		cmd, submitted := s.command.Update(msg)
		if submitted {
			s.commandValue = s.command.Value()
			s.step = 4
			return s, nil
		}
		return s, cmd
	case 4:
		menu, cmd := s.enableMenu.Update(msg)
		s.enableMenu = menu
		if cmd != nil {
			selected := cmd().(components.MenuSelectedMsg)
			s.enabled = selected.Key == "yes"
			s.step = 5
		}
		return s, nil
	case 5:
		if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
			input := cron.JobInput{
				Name:        strptr(s.nameValue),
				Description: strptr(s.descValue),
				Schedule:    strptr(s.schedule),
				Command:     strptr(s.commandValue),
				Enabled:     boolptr(s.enabled),
			}
			return s, func() tea.Msg {
				job, err := s.service.Add(input)
				return addSubmitMsg{job: job, err: err}
			}
		}
	}
	return s, nil
}

func (s *addScreen) View() string {
	if s.created != nil {
		return s.theme.RenderSuccess("Cron job created successfully.") + "\n\n" +
			renderReview(s.theme, "Created job", [][2]string{
				{"ID", s.created.ID},
				{"Name", s.created.Name},
				{"Schedule", s.created.Schedule},
				{"Command", s.created.Command},
				{"Enabled", fmt.Sprintf("%t", s.created.Enabled)},
			}, nil)
	}

	parts := []string{s.theme.SubtitleStyle.Render(stepLabel(s.step))}
	switch s.step {
	case 0:
		parts = append(parts, s.name.View())
	case 1:
		parts = append(parts, s.description.View())
	case 2:
		parts = append(parts, s.theme.LabelStyle.Render("Schedule"), s.presetMenu.View())
	case 21:
		parts = append(parts, s.theme.LabelStyle.Render("Schedule"), s.custom.View(), s.theme.RenderHint(scheduleSummary(s.custom.Value())))
	case 3:
		parts = append(parts, s.command.View())
		if warning := dangerousCommandWarning(s.command.Value()); warning != "" {
			parts = append(parts, s.theme.HintStyle.Foreground(s.theme.Warning).Render(warning))
		}
	case 4:
		parts = append(parts, s.theme.LabelStyle.Render("Enable immediately?"), s.enableMenu.View())
	case 5:
		parts = append(parts, renderReview(s.theme, "Review", [][2]string{
			{"Name", s.nameValue},
			{"Description", blankOrValue(s.descValue)},
			{"Schedule", s.schedule},
			{"Summary", scheduleSummary(s.schedule)},
			{"Command", s.commandValue},
			{"Enabled", fmt.Sprintf("%t", s.enabled)},
		}, nil))
	}
	if s.err != "" {
		parts = append(parts, "", s.theme.RenderError(s.err))
	}
	return strings.Join(parts, "\n\n")
}

func stepLabel(step int) string {
	switch step {
	case 0:
		return "Step 1 of 5"
	case 1:
		return "Step 2 of 5"
	case 2, 21:
		return "Step 3 of 5"
	case 3:
		return "Step 4 of 5"
	case 4:
		return "Step 5 of 5"
	default:
		return "Review"
	}
}

func blankOrValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(empty)"
	}
	return v
}
