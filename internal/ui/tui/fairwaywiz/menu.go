package fairwaywiz

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type menuScreen struct {
	theme  theme.Theme
	client FairwayClient
	menu   components.Menu
	width  int
}

func newMenuScreen(th theme.Theme, client FairwayClient) Screen {
	items := []components.MenuItem{
		{Title: "Manage routes", Description: "Create, inspect, edit and delete HTTP entrypoints.", Key: "routes", BadgeVariant: "accent"},
		{Title: "View status", Description: "Inspect daemon bind, uptime and current route count.", Key: "status"},
		{Title: "Edit daemon config", Description: "Reserved for a future daemon settings editor.", Key: "daemon", Disabled: true, Badge: "soon"},
		{Title: "Quit", Description: "Leave the fairway configuration wizard.", Key: "quit"},
	}
	return &menuScreen{
		theme:  th,
		client: client,
		menu:   components.NewMenu(th, items),
	}
}

func (s *menuScreen) Init() tea.Cmd        { return nil }
func (s *menuScreen) Title() string        { return "Fairway Config" }
func (s *menuScreen) Breadcrumb() []string { return []string{"fairway", "config"} }
func (s *menuScreen) Footer() []components.KeyHint {
	return []components.KeyHint{{Key: "↑↓", Label: "navigate"}, {Key: "enter", Label: "select"}}
}
func (s *menuScreen) State() state { return stateMenu }

func (s *menuScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if resize, ok := msg.(tea.WindowSizeMsg); ok {
		s.width = resize.Width
		s.menu = s.menu.SetWidth(resize.Width)
	}
	menu, cmd := s.menu.Update(msg)
	s.menu = menu
	if cmd == nil {
		return s, nil
	}
	selected := cmd().(components.MenuSelectedMsg)
	switch selected.Key {
	case "routes":
		next := newListScreen(s.theme, s.client)
		return next, next.Init()
	case "status":
		next := newStatusScreen(s.theme, s.client)
		return next, next.Init()
	case "quit":
		return s, tea.Quit
	default:
		return s, nil
	}
}

func (s *menuScreen) View() string {
	intro := s.theme.SubtitleStyle.Render("Keyboard-driven route management with the same socket-backed fairway client used by the CLI.")
	return strings.Join([]string{intro, "", s.menu.View()}, "\n")
}
