package servicewiz

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	svcpkg "github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type updateSubmitMsg struct {
	record svcpkg.ServiceRecord
	err    error
}

type updateScreen struct {
	theme      theme.Theme
	service    ServiceAPI
	selectID   string
	picker     components.Menu
	target     *svcpkg.ServiceRecord
	add        *addScreen
	review     components.Confirm
	spinner    components.Spinner
	err        string
	updated    *svcpkg.ServiceRecord
	submitting bool
}

func newUpdateScreen(th theme.Theme, service ServiceAPI, preselectedID string) Screen {
	s := &updateScreen{
		theme:    th,
		service:  service,
		selectID: preselectedID,
		review:   components.NewConfirm(th, "Save these changes or go back to edit.", false),
		spinner:  components.NewSpinner(th, "Updating service…"),
	}
	s.bootstrap()
	return s
}

func (s *updateScreen) bootstrap() {
	if s.selectID != "" {
		if record, err := s.service.Get(s.selectID); err == nil {
			s.target = &record
			s.seed(record)
			return
		}
	}
	records, _ := s.service.List()
	s.picker = components.NewMenu(s.theme, recordsToMenuItems(records))
}

func (s *updateScreen) seed(record svcpkg.ServiceRecord) {
	screen := newAddScreen(s.theme, s.service, nil).(*addScreen)
	screen.name.SetValue(record.Name)
	screen.description.SetValue(record.Description)
	screen.command.SetValue(record.Command)
	screen.workingDir.SetValue(record.WorkingDir)
	screen.environment.SetValue(envSummary(record.Environment))
	screen.nameValue = record.Name
	screen.descValue = record.Description
	screen.commandValue = record.Command
	screen.workingValue = record.WorkingDir
	screen.envValue = envSummary(record.Environment)
	screen.autoRestartOn = record.AutoRestart
	screen.enabled = record.Enabled
	screen.step = 0
	s.add = screen
}

func (s *updateScreen) Init() tea.Cmd { return nil }
func (s *updateScreen) Title() string { return "Update Service" }
func (s *updateScreen) Breadcrumb() []string { return []string{"service", "update"} }
func (s *updateScreen) Footer() []components.KeyHint {
	switch {
	case s.target == nil:
		return []components.KeyHint{{Key: "↑↓", Label: "choose"}, {Key: "enter", Label: "select"}}
	case s.updated != nil:
		return []components.KeyHint{{Key: "enter", Label: "return to menu"}}
	case s.submitting:
		return []components.KeyHint{{Key: "esc", Label: "cancel"}}
	case s.add != nil && s.add.step == 7:
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
		s.updated = &msg.record
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
			case s.add != nil && s.add.step == 7:
				s.add.step = 6
				s.err = ""
				return s, nil
			}
		}
		if s.updated != nil && msg.String() == "enter" {
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.add != nil && s.add.step == 7 && s.err != "" && msg.String() == "r" {
			s.err = ""
			s.submitting = true
			return s, s.submit()
		}
	}
	if s.target == nil {
		records, _ := s.service.List()
		if len(records) == 0 {
			empty := components.NewEmpty(s.theme, components.EmptyProps{Icon: "⎋", Title: "No services to update.", Hint: "[esc] back"})
			if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
				return newMenuScreen(s.theme, s.service), nil
			}
			s.err = empty.View()
			return s, nil
		}
		menu, cmd := s.picker.Update(msg)
		s.picker = menu
		if cmd != nil {
			record, err := s.service.Get(cmd().(components.MenuSelectedMsg).Key)
			if err != nil {
				s.err = err.Error()
				return s, nil
			}
			s.target = &record
			s.seed(record)
			return s, s.add.Init()
		}
		return s, nil
	}
	if s.updated != nil {
		return s, nil
	}
	if s.add.step < 7 {
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
			s.add.step = 6
			return s, nil
		}
		s.err = ""
		s.submitting = true
		return s, s.submit()
	}
	return s, nil
}

func (s *updateScreen) View() string {
	if s.err != "" && s.target == nil && strings.Contains(s.err, "No services to update.") {
		return s.err
	}
	if s.target == nil {
		return strings.Join([]string{s.theme.LabelStyle.Render("Select a service to update"), s.picker.View()}, "\n\n")
	}
	if s.updated != nil {
		return s.theme.RenderSuccess("Service updated successfully.") + "\n\n" +
			renderReview(s.theme, "Updated service", [][2]string{
				{"Name", s.updated.Name},
				{"Description", blankOrValue(s.updated.Description)},
				{"Command", s.updated.Command},
				{"Working Dir", blankOrValue(s.updated.WorkingDir)},
				{"Enabled", boolLabel(s.updated.Enabled)},
			}, s.changedFields(*s.updated))
	}
	if s.add.step < 7 {
		if s.err != "" {
			return s.add.View() + "\n\n" + s.theme.RenderError(s.err)
		}
		return s.add.View()
	}
	envMap, _ := parseEnvCSV(s.add.envValue)
	parts := []string{
		s.theme.SubtitleStyle.Render("Review changes"),
		renderReview(s.theme, "Update review", [][2]string{
			{"Name", s.add.nameValue},
			{"Description", blankOrValue(s.add.descValue)},
			{"Command", s.add.commandValue},
			{"Working Dir", blankOrValue(s.add.workingValue)},
			{"Environment", envSummary(envMap)},
			{"Auto Restart", boolLabel(s.add.autoRestartOn)},
			{"Enabled", boolLabel(s.add.enabled)},
		}, s.changedFields(s.previewRecord(envMap))),
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

func (s *updateScreen) previewRecord(env map[string]string) svcpkg.ServiceRecord {
	return svcpkg.ServiceRecord{
		ID:          s.target.ID,
		Name:        s.add.nameValue,
		Description: s.add.descValue,
		Command:     s.add.commandValue,
		WorkingDir:  s.add.workingValue,
		Environment: env,
		AutoRestart: s.add.autoRestartOn,
		Enabled:     s.add.enabled,
	}
}

func (s *updateScreen) changedFields(record svcpkg.ServiceRecord) map[string]bool {
	return map[string]bool{
		"Name":         s.target.Name != record.Name,
		"Description":  s.target.Description != record.Description,
		"Command":      s.target.Command != record.Command,
		"Working Dir":  s.target.WorkingDir != record.WorkingDir,
		"Environment":  envSummary(s.target.Environment) != envSummary(record.Environment),
		"Auto Restart": s.target.AutoRestart != record.AutoRestart,
		"Enabled":      s.target.Enabled != record.Enabled,
	}
}

func (s *updateScreen) submit() tea.Cmd {
	envMap, err := parseEnvCSV(s.add.envValue)
	if err != nil {
		return func() tea.Msg { return updateSubmitMsg{err: err} }
	}
	patch := svcpkg.ServiceInput{}
	if s.target.Name != s.add.nameValue {
		patch.Name = strptr(s.add.nameValue)
	}
	if s.target.Description != s.add.descValue {
		patch.Description = strptr(s.add.descValue)
	}
	if s.target.Command != s.add.commandValue {
		patch.Command = strptr(s.add.commandValue)
	}
	if s.target.WorkingDir != s.add.workingValue {
		patch.WorkingDir = strptr(s.add.workingValue)
	}
	if envSummary(s.target.Environment) != envSummary(envMap) {
		patch.Environment = envptr(envMap)
	}
	if s.target.AutoRestart != s.add.autoRestartOn {
		patch.AutoRestart = boolptr(s.add.autoRestartOn)
	}
	if s.target.Enabled != s.add.enabled {
		patch.Enabled = boolptr(s.add.enabled)
	}
	return func() tea.Msg {
		record, err := s.service.Update(s.target.ID, patch)
		return updateSubmitMsg{record: record, err: err}
	}
}
