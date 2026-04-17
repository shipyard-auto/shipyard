package fairwaywiz

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type deleteScreen struct {
	theme    theme.Theme
	client   FairwayClient
	target   fairwayctl.Route
	confirm  components.Confirm
	err      string
	donePath string
}

func newDeleteScreen(th theme.Theme, client FairwayClient, route fairwayctl.Route) Screen {
	return &deleteScreen{
		theme:   th,
		client:  client,
		target:  route,
		confirm: components.NewConfirm(th, fmt.Sprintf("Delete route %s? This cannot be undone.", route.Path), true),
	}
}

func (s *deleteScreen) Init() tea.Cmd { return nil }
func (s *deleteScreen) Title() string { return "Delete Route" }
func (s *deleteScreen) Breadcrumb() []string {
	return []string{"fairway", "config", "routes", "delete"}
}
func (s *deleteScreen) Footer() []components.KeyHint {
	if s.donePath != "" {
		return []components.KeyHint{{Key: "enter", Label: "back to routes"}}
	}
	return []components.KeyHint{{Key: "←→", Label: "choose"}, {Key: "enter", Label: "confirm"}, {Key: "esc", Label: "cancel"}}
}
func (s *deleteScreen) State() state { return stateDelete }

func (s *deleteScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case routeDeleteMsg:
		if msg.err != nil {
			s.err = humanizeRouteError(msg.err, msg.path)
			return s, nil
		}
		s.donePath = msg.path
		s.err = ""
		return s, nil
	case tea.KeyMsg:
		if msg.String() == "esc" {
			return newListScreen(s.theme, s.client), loadRoutesCmd(s.client)
		}
		if s.donePath != "" && msg.String() == "enter" {
			return newListScreen(s.theme, s.client), loadRoutesCmd(s.client)
		}
	}
	confirm, cmd := s.confirm.Update(msg)
	s.confirm = confirm
	if cmd == nil {
		return s, nil
	}
	decision := cmd().(components.ConfirmMsg)
	if !decision.Accepted {
		return newListScreen(s.theme, s.client), loadRoutesCmd(s.client)
	}
	return s, deleteRouteCmd(s.client, s.target.Path)
}

func (s *deleteScreen) View() string {
	if s.donePath != "" {
		return s.theme.RenderSuccess("Route deleted successfully.") + "\n\n" + routeDetail(s.theme, s.target)
	}
	body := s.confirm.View()
	if s.err != "" {
		body += "\n\n" + s.theme.RenderError(s.err)
	}
	return body
}
