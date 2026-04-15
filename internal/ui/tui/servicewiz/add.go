package servicewiz

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	svcpkg "github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type addSubmitMsg struct {
	record svcpkg.ServiceRecord
	err    error
}

type addScreen struct {
	theme         theme.Theme
	service       ServiceAPI
	name          components.Input
	description   components.Input
	command       components.Input
	workingDir    components.Input
	environment   components.Input
	autoRestart   components.Menu
	enabledMenu   components.Menu
	review        components.Confirm
	spinner       components.Spinner
	step          int
	nameValue     string
	descValue     string
	commandValue  string
	workingValue  string
	envValue      string
	autoRestartOn bool
	enabled       bool
	err           string
	created       *svcpkg.ServiceRecord
	submitting    bool
}

func newAddScreen(th theme.Theme, service ServiceAPI, _ *svcpkg.ServiceRecord) Screen {
	name := components.NewInput(th, "Service name", "Gateway", func(v string) error { return validateSingleLineRequired("name", v) })
	description := components.NewInput(th, "Description (optional)", "Optional context", func(v string) error { return validateOptionalSingleLine("description", v) })
	command := components.NewInput(th, "Command", "/bin/sh -lc 'python app.py'", func(v string) error { return validateSingleLineRequired("command", v) })
	workingDir := components.NewInput(th, "Working dir (optional)", "/Users/me/project", validateWorkingDir)
	environment := components.NewInput(th, "Environment vars", "FOO=BAR, API_URL=https://...", func(v string) error {
		_, err := parseEnvCSV(v)
		return err
	})
	name.SetHint("A short human-readable label for this service.")
	description.SetHint("Press Enter to skip.")
	command.SetHint("The shell command to run. No multi-line values.")
	workingDir.SetHint("Optional absolute path.")
	environment.SetHint("Comma-separated KEY=VALUE pairs. Press Enter to skip.")
	autoRestart := components.NewMenu(th, []components.MenuItem{
		{Title: "Yes", Description: "Restart on failure", Key: "yes"},
		{Title: "No", Description: "Do not restart automatically", Key: "no"},
	})
	enabledMenu := components.NewMenu(th, []components.MenuItem{
		{Title: "Yes", Description: "Enable at user login", Key: "yes"},
		{Title: "No", Description: "Keep disabled at login", Key: "no"},
	})
	review := components.NewConfirm(th, "Confirm creation or go back to edit.", false)
	spinner := components.NewSpinner(th, "Creating service…")
	return &addScreen{
		theme:       th,
		service:     service,
		name:        name,
		description: description,
		command:     command,
		workingDir:  workingDir,
		environment: environment,
		autoRestart: autoRestart,
		enabledMenu: enabledMenu,
		review:      review,
		spinner:     spinner,
		enabled:     true,
	}
}

func (s *addScreen) Init() tea.Cmd { return s.name.Init() }
func (s *addScreen) Title() string { return "Add Service" }
func (s *addScreen) Breadcrumb() []string { return []string{"service", "add"} }
func (s *addScreen) Footer() []components.KeyHint {
	switch {
	case s.created != nil:
		return []components.KeyHint{{Key: "enter", Label: "return to menu"}}
	case s.submitting:
		return []components.KeyHint{{Key: "esc", Label: "cancel"}}
	case s.step == 5 || s.step == 6:
		return []components.KeyHint{{Key: "↑↓", Label: "choose"}, {Key: "enter", Label: "select"}}
	case s.step == 7:
		if s.err != "" {
			return []components.KeyHint{{Key: "r", Label: "retry"}, {Key: "esc", Label: "cancel"}}
		}
		return []components.KeyHint{{Key: "←→", Label: "choose"}, {Key: "enter", Label: "select"}}
	default:
		return []components.KeyHint{{Key: "enter", Label: "next"}}
	}
}

func (s *addScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case addSubmitMsg:
		s.submitting = false
		if msg.err != nil {
			s.err = msg.err.Error()
			return s, nil
		}
		s.created = &msg.record
		s.err = ""
		return s, nil
	case tea.KeyMsg:
		if s.submitting && msg.String() == "esc" {
			s.submitting = false
			s.err = "creation cancelled"
			return s, nil
		}
		if msg.String() == "esc" {
			if s.created != nil {
				return newMenuScreen(s.theme, s.service), nil
			}
			if s.step == 7 {
				s.step = 6
				return s, nil
			}
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.created != nil && msg.String() == "enter" {
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.step == 7 && s.err != "" && msg.String() == "r" {
			s.err = ""
			s.submitting = true
			return s, s.submit()
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
			return s, s.command.Init()
		}
		return s, cmd
	case 2:
		cmd, submitted := s.command.Update(msg)
		if submitted {
			s.commandValue = s.command.Value()
			s.step = 3
			return s, s.workingDir.Init()
		}
		return s, cmd
	case 3:
		cmd, submitted := s.workingDir.Update(msg)
		if submitted {
			s.workingValue = s.workingDir.Value()
			s.step = 4
			return s, s.environment.Init()
		}
		return s, cmd
	case 4:
		cmd, submitted := s.environment.Update(msg)
		if submitted {
			s.envValue = s.environment.Value()
			s.step = 5
		}
		return s, cmd
	case 5:
		menu, cmd := s.autoRestart.Update(msg)
		s.autoRestart = menu
		if cmd != nil {
			s.autoRestartOn = cmd().(components.MenuSelectedMsg).Key == "yes"
			s.step = 6
		}
		return s, nil
	case 6:
		menu, cmd := s.enabledMenu.Update(msg)
		s.enabledMenu = menu
		if cmd != nil {
			s.enabled = cmd().(components.MenuSelectedMsg).Key == "yes"
			s.step = 7
		}
		return s, nil
	case 7:
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
				s.step = 6
				return s, nil
			}
			s.err = ""
			s.submitting = true
			return s, s.submit()
		}
	}
	return s, nil
}

func (s *addScreen) View() string {
	if s.created != nil {
		return s.theme.RenderSuccess("Service created successfully.") + "\n\n" +
			renderReview(s.theme, "Created service", [][2]string{
				{"ID", s.created.ID},
				{"Name", s.created.Name},
				{"Command", s.created.Command},
				{"Enabled", boolLabel(s.created.Enabled)},
			}, nil)
	}
	parts := []string{s.theme.SubtitleStyle.Render(stepLabel(s.step))}
	switch s.step {
	case 0:
		parts = append(parts, s.name.View())
	case 1:
		parts = append(parts, s.description.View())
	case 2:
		parts = append(parts, s.command.View())
		if warning := dangerousCommandWarning(s.command.Value()); warning != "" {
			parts = append(parts, s.theme.HintStyle.Foreground(s.theme.Warning).Render(warning))
		}
	case 3:
		parts = append(parts, s.workingDir.View())
	case 4:
		parts = append(parts, s.environment.View())
	case 5:
		parts = append(parts, s.theme.LabelStyle.Render("Auto restart?"), s.autoRestart.View())
	case 6:
		parts = append(parts, s.theme.LabelStyle.Render("Enabled at login?"), s.enabledMenu.View())
	case 7:
		envMap, _ := parseEnvCSV(s.envValue)
		parts = append(parts, renderReview(s.theme, "Review", [][2]string{
			{"Name", s.nameValue},
			{"Description", blankOrValue(s.descValue)},
			{"Command", s.commandValue},
			{"Working Dir", blankOrValue(s.workingValue)},
			{"Environment", envSummary(envMap)},
			{"Auto Restart", boolLabel(s.autoRestartOn)},
			{"Enabled", boolLabel(s.enabled)},
		}, nil))
		if s.submitting {
			parts = append(parts, s.spinner.View())
		} else {
			parts = append(parts, s.review.View())
		}
	}
	if s.err != "" {
		parts = append(parts, "", s.theme.RenderError(s.err))
	}
	return strings.Join(parts, "\n\n")
}

func (s *addScreen) submit() tea.Cmd {
	envMap, err := parseEnvCSV(s.envValue)
	if err != nil {
		return func() tea.Msg { return addSubmitMsg{err: err} }
	}
	input := svcpkg.ServiceInput{
		Name:        strptr(s.nameValue),
		Description: strptr(s.descValue),
		Command:     strptr(s.commandValue),
		WorkingDir:  strptr(s.workingValue),
		AutoRestart: boolptr(s.autoRestartOn),
		Enabled:     boolptr(s.enabled),
	}
	if envMap != nil {
		input.Environment = envptr(envMap)
	}
	return func() tea.Msg {
		record, err := s.service.Add(input)
		return addSubmitMsg{record: record, err: err}
	}
}

func stepLabel(step int) string {
	switch step {
	case 0:
		return "Step 1 of 7"
	case 1:
		return "Step 2 of 7"
	case 2:
		return "Step 3 of 7"
	case 3:
		return "Step 4 of 7"
	case 4:
		return "Step 5 of 7"
	case 5:
		return "Step 6 of 7"
	case 6:
		return "Step 7 of 7"
	default:
		return "Review"
	}
}
