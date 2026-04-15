package logwiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type menuScreen struct {
	theme   theme.Theme
	service LogsService
	menu    components.Menu
	empty   *components.Empty
	sources []logs.SourceSummary
}

func newMenuScreen(th theme.Theme, service LogsService) Screen {
	s := &menuScreen{theme: th, service: service}
	s.refresh()
	return s
}

func (s *menuScreen) refresh() {
	sources, _ := s.service.ListSources()
	s.sources = sources
	count := len(sources)
	items := []components.MenuItem{
		{Title: "View sources", Badge: fmt.Sprintf("%d", count), Disabled: count == 0, Key: "sources"},
		{Title: "Show recent events", Disabled: count == 0, Key: "show"},
		{Title: "Tail live events", Disabled: count == 0, Key: "tail"},
		{Title: "Configure retention", Key: "retention"},
		{Title: "Prune old logs", Disabled: count == 0, Key: "prune"},
		{Title: "Exit", Key: "exit"},
	}
	s.menu = components.NewMenu(s.theme, items)
	if count == 0 {
		empty := components.NewEmpty(s.theme, components.EmptyProps{
			Icon:        "⎋",
			Title:       "No logs yet.",
			Description: "Shipyard writes events automatically as subsystems run.",
			Hint:        "Use the cron wizard to create and run a job to produce log events.",
		})
		s.empty = &empty
	} else {
		s.empty = nil
	}
}

func (s *menuScreen) Init() tea.Cmd                       { return nil }
func (s *menuScreen) Title() string                       { return "Logs Control Panel" }
func (s *menuScreen) Breadcrumb() []string                { return []string{"logs"} }
func (s *menuScreen) Footer() []components.KeyHint        { return []components.KeyHint{{Key: "↑↓", Label: "navigate"}, {Key: "enter", Label: "select"}} }
func (s *menuScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		return s, nil
	}
	menu, cmd := s.menu.Update(msg)
	s.menu = menu
	if cmd != nil {
		selected := cmd().(components.MenuSelectedMsg)
		switch selected.Key {
		case "sources":
			return newSourcesScreen(s.theme, s.service), nil
		case "show":
			return newShowScreen(s.theme, s.service, ""), nil
		case "tail":
			return newTailScreen(s.theme, s.service), nil
		case "retention":
			return newRetentionScreen(s.theme, s.service), nil
		case "prune":
			return newPruneScreen(s.theme, s.service), nil
		case "exit":
			return s, tea.Quit
		}
	}
	return s, nil
}

func (s *menuScreen) View() string {
	parts := []string{}
	if s.empty != nil {
		parts = append(parts, s.empty.View(), "")
	}
	parts = append(parts, s.menu.View())
	return strings.Join(parts, "\n")
}
