package fairwaywiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type statusScreen struct {
	theme  theme.Theme
	client FairwayClient
	status fairwayctl.StatusInfo
	routes []fairwayctl.Route
	err    string
	loaded bool
}

func newStatusScreen(th theme.Theme, client FairwayClient) Screen {
	return &statusScreen{theme: th, client: client}
}

func (s *statusScreen) Init() tea.Cmd        { return loadStatusCmd(s.client) }
func (s *statusScreen) Title() string        { return "Daemon Status" }
func (s *statusScreen) Breadcrumb() []string { return []string{"fairway", "config", "status"} }
func (s *statusScreen) Footer() []components.KeyHint {
	return []components.KeyHint{{Key: "r", Label: "refresh"}, {Key: "q", Label: "quit"}, {Key: "esc", Label: "back"}}
}
func (s *statusScreen) State() state { return stateStatus }

func (s *statusScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case statusLoadedMsg:
		if msg.err != nil {
			s.err = msg.err.Error()
			s.loaded = false
			return s, nil
		}
		s.status = msg.status
		s.routes = append([]fairwayctl.Route{}, msg.routes...)
		s.loaded = true
		s.err = ""
		return s, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return newMenuScreen(s.theme, s.client), nil
		case "r":
			return s, loadStatusCmd(s.client)
		}
	}
	return s, nil
}

func (s *statusScreen) View() string {
	if s.err != "" {
		return s.theme.RenderError(s.err)
	}
	if !s.loaded {
		return s.theme.RenderHint("Loading fairway status…")
	}
	lines := [][2]string{
		{"Version", blankOr(s.status.Version)},
		{"Uptime", blankOr(s.status.Uptime)},
		{"Listen", fmt.Sprintf("%s:%d", blankOr(s.status.Bind), s.status.Port)},
		{"In flight", fmt.Sprintf("%d", s.status.InFlight)},
		{"Routes", fmt.Sprintf("%d", s.status.RouteCount)},
	}
	body := []string{renderReview(s.theme, "Daemon", lines)}
	if len(s.routes) == 0 {
		body = append(body, s.theme.PanelStyle.Render(s.theme.RenderHint("No routes configured yet.")))
	} else {
		body = append(body, s.theme.PanelStyle.Render(s.theme.ValueStyle.Render("Configured paths")+"\n\n"+strings.Join(routePaths(s.routes), "\n")))
	}
	return strings.Join(body, "\n\n")
}

func blankOr(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(empty)"
	}
	return v
}

func routePaths(routes []fairwayctl.Route) []string {
	out := make([]string, 0, len(routes))
	for _, route := range routes {
		out = append(out, "• "+route.Path)
	}
	return out
}
