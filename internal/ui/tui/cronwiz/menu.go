package cronwiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type menuScreen struct {
	theme   theme.Theme
	service CronService
	menu    components.Menu
	empty   *components.Empty
	jobs    []cron.Job
	err     error
}

func newMenuScreen(th theme.Theme, service CronService) Screen {
	s := &menuScreen{theme: th, service: service}
	s.refresh()
	return s
}

func (s *menuScreen) refresh() {
	jobs, err := s.service.List()
	s.jobs = jobs
	s.err = err

	disabled := 0
	enabled := 0
	for _, job := range jobs {
		if job.Enabled {
			enabled++
		} else {
			disabled++
		}
	}

	items := []components.MenuItem{
		{Title: "Add new cron job", Description: "+", Key: "add"},
		{Title: "Browse jobs", Description: "≡", Badge: countBadge(len(jobs), "empty"), Disabled: len(jobs) == 0, Key: "browse"},
		{Title: "Update a job", Disabled: len(jobs) == 0, Key: "update"},
		{Title: "Enable jobs", Disabled: disabled == 0, Badge: fmt.Sprintf("%d", disabled), Key: "enable"},
		{Title: "Disable jobs", Disabled: enabled == 0, Badge: fmt.Sprintf("%d", enabled), Key: "disable"},
		{Title: "Run a job now", Disabled: len(jobs) == 0, Key: "run"},
		{Title: "Delete a job", Disabled: len(jobs) == 0, Key: "delete"},
		{Title: "Exit", Key: "exit"},
	}
	s.menu = components.NewMenu(s.theme, items)
	if len(jobs) == 0 {
		empty := components.NewEmpty(s.theme, components.EmptyProps{
			Icon:        "⛵",
			Title:       "No cron jobs yet — start by adding one.",
			Description: "",
			Hint:        "Choose Add new cron job to begin.",
		})
		s.empty = &empty
	} else {
		s.empty = nil
	}
}

func countBadge(count int, empty string) string {
	if count == 0 {
		return empty
	}
	return fmt.Sprintf("%d", count)
}

func (s *menuScreen) Init() tea.Cmd { return nil }

func (s *menuScreen) Title() string { return "Cron Control Panel" }

func (s *menuScreen) Breadcrumb() []string { return []string{"cron"} }

func (s *menuScreen) Footer() []components.KeyHint {
	return []components.KeyHint{
		{Key: "↑↓", Label: "navigate"},
		{Key: "enter", Label: "select"},
	}
}

func (s *menuScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "esc" {
		return s, nil
	}
	menu, cmd := s.menu.Update(msg)
	s.menu = menu
	if cmd != nil {
		if selected, ok := cmd().(components.MenuSelectedMsg); ok {
			switch selected.Key {
			case "add":
				return newAddScreen(s.theme, s.service, nil), nil
			case "browse":
				return newListScreen(s.theme, s.service), nil
			case "update":
				return newUpdateScreen(s.theme, s.service, ""), nil
			case "enable":
				return newEnableScreen(s.theme, s.service), nil
			case "disable":
				return newDisableScreen(s.theme, s.service), nil
			case "run":
				return newRunScreen(s.theme, s.service, ""), nil
			case "delete":
				return newDeleteScreen(s.theme, s.service, ""), nil
			case "exit":
				return s, tea.Quit
			}
		}
	}
	return s, nil
}

func (s *menuScreen) View() string {
	if s.err != nil {
		return s.theme.RenderError(s.err.Error())
	}
	parts := []string{}
	if s.empty != nil {
		parts = append(parts, s.empty.View(), "")
	}
	parts = append(parts, s.menu.View())
	return strings.Join(parts, "\n")
}
