package servicewiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	svcpkg "github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type menuScreen struct {
	theme   theme.Theme
	service ServiceAPI
	menu    components.Menu
	empty   *components.Empty
	records []svcpkg.ServiceRecord
	err     error
	width   int
}

func newMenuScreen(th theme.Theme, service ServiceAPI) Screen {
	s := &menuScreen{theme: th, service: service}
	s.refresh()
	return s
}

func (s *menuScreen) refresh() {
	records, err := s.service.List()
	s.records, s.err = records, err
	enabled := 0
	disabled := 0
	for _, record := range records {
		if record.Enabled {
			enabled++
		} else {
			disabled++
		}
	}
	items := []components.MenuItem{
		{Title: "Add service", Description: "Create a new Shipyard-managed service.", Key: "add"},
		{Title: "Browse services", Description: "Inspect services and runtime state.", Badge: countBadge(len(records)), Disabled: len(records) == 0, Key: "browse"},
		{Title: "Update service", Description: "Edit an existing service definition.", Disabled: len(records) == 0, Key: "update"},
		{Title: "Start service", Description: "Start a service immediately.", Disabled: len(records) == 0, Key: "start"},
		{Title: "Stop service", Description: "Stop a running service.", Disabled: len(records) == 0, Key: "stop"},
		{Title: "Restart service", Description: "Restart a service immediately.", Disabled: len(records) == 0, Key: "restart"},
		{Title: "Enable at login", Description: "Enable services at user login.", Disabled: disabled == 0, Badge: fmt.Sprintf("%d", disabled), Key: "enable"},
		{Title: "Disable at login", Description: "Disable login startup.", Disabled: enabled == 0, Badge: fmt.Sprintf("%d", enabled), Key: "disable"},
		{Title: "Status", Description: "Inspect one service runtime status.", Disabled: len(records) == 0, Key: "status"},
		{Title: "Delete service", Description: "Remove a Shipyard-managed service.", Disabled: len(records) == 0, Key: "delete"},
		{Title: "Exit", Description: "Leave the service control panel.", Key: "exit"},
	}
	s.menu = components.NewMenu(s.theme, items).SetWidth(s.width)
	if len(records) == 0 {
		empty := components.NewEmpty(s.theme, components.EmptyProps{
			Icon:        "⚓",
			Title:       "No services yet",
			Description: "Shipyard only manages services it creates itself.",
			Hint:        "Choose Add service to begin.",
		}).SetWidth(s.width)
		s.empty = &empty
	} else {
		s.empty = nil
	}
}

func countBadge(count int) string {
	if count == 0 {
		return "empty"
	}
	return fmt.Sprintf("%d", count)
}

func (s *menuScreen) Init() tea.Cmd { return nil }
func (s *menuScreen) Title() string { return "Service Control Panel" }
func (s *menuScreen) Breadcrumb() []string { return []string{"service"} }
func (s *menuScreen) Footer() []components.KeyHint {
	return []components.KeyHint{{Key: "↑↓", Label: "navigate"}, {Key: "enter", Label: "select"}}
}

func (s *menuScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if resize, ok := msg.(tea.WindowSizeMsg); ok {
		s.width = resize.Width
		s.menu = s.menu.SetWidth(resize.Width)
		if s.empty != nil {
			updated := s.empty.SetWidth(resize.Width)
			s.empty = &updated
		}
	}
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		return s, nil
	}
	menu, cmd := s.menu.Update(msg)
	s.menu = menu
	if cmd != nil {
		selected := cmd().(components.MenuSelectedMsg)
		switch selected.Key {
		case "add":
			return newAddScreen(s.theme, s.service, nil), nil
		case "browse":
			return newListScreen(s.theme, s.service), nil
		case "update":
			return newUpdateScreen(s.theme, s.service, ""), nil
		case "start":
			return newStartScreen(s.theme, s.service), nil
		case "stop":
			return newStopScreen(s.theme, s.service), nil
		case "restart":
			return newRestartScreen(s.theme, s.service), nil
		case "enable":
			return newEnableScreen(s.theme, s.service), nil
		case "disable":
			return newDisableScreen(s.theme, s.service), nil
		case "status":
			return newStatusScreen(s.theme, s.service, ""), nil
		case "delete":
			return newDeleteScreen(s.theme, s.service, ""), nil
		case "exit":
			return s, tea.Quit
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
