package fairwaywiz

import (
	"fmt"
	"strings"

	btable "github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type listScreen struct {
	theme   theme.Theme
	client  FairwayClient
	table   *components.Table
	empty   *components.Empty
	routes  []fairwayctl.Route
	detail  string
	err     string
	loading bool
}

func newListScreen(th theme.Theme, client FairwayClient) Screen {
	return &listScreen{
		theme:   th,
		client:  client,
		loading: true,
	}
}

func (s *listScreen) Init() tea.Cmd        { return loadRoutesCmd(s.client) }
func (s *listScreen) Title() string        { return "Manage Routes" }
func (s *listScreen) Breadcrumb() []string { return []string{"fairway", "config", "routes"} }
func (s *listScreen) Footer() []components.KeyHint {
	return []components.KeyHint{
		{Key: "n", Label: "new"},
		{Key: "e", Label: "edit"},
		{Key: "d", Label: "delete"},
		{Key: "enter", Label: "details"},
		{Key: "esc", Label: "back"},
	}
}
func (s *listScreen) State() state { return stateList }

func (s *listScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case routesLoadedMsg:
		s.loading = false
		if msg.err != nil {
			s.err = msg.err.Error()
			return s, nil
		}
		s.routes = append([]fairwayctl.Route{}, msg.routes...)
		s.buildTable()
		return s, nil
	case tea.WindowSizeMsg:
		if s.table != nil {
			table := *s.table
			table.Resize(msg.Width, msg.Height)
			s.table = &table
		}
		if s.empty != nil {
			empty := s.empty.SetWidth(msg.Width)
			s.empty = &empty
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return newMenuScreen(s.theme, s.client), nil
		case "n":
			return newFormScreen(s.theme, s.client, nil), nil
		case "e":
			if route := s.selectedRoute(); route != nil {
				return newFormScreen(s.theme, s.client, route), nil
			}
		case "d":
			if route := s.selectedRoute(); route != nil {
				return newDeleteScreen(s.theme, s.client, *route), nil
			}
		}
	}
	if s.loading || s.empty != nil || s.table == nil {
		return s, nil
	}
	table, cmd := s.table.Update(msg)
	s.table = &table
	if cmd != nil {
		selected := s.selectedRoute()
		if selected == nil {
			return s, nil
		}
		s.detail = routeDetail(s.theme, *selected)
	}
	if selected := s.selectedRoute(); selected != nil && s.detail == "" {
		s.detail = routeDetail(s.theme, *selected)
	}
	return s, nil
}

func (s *listScreen) View() string {
	if s.loading {
		return s.theme.RenderHint("Loading routes…")
	}
	if s.err != "" {
		return s.theme.RenderError(s.err)
	}
	if s.empty != nil {
		return s.empty.View()
	}
	heading := s.theme.ValueStyle.Render(fmt.Sprintf("Routes (%d)", len(s.routes)))
	if s.detail == "" {
		return strings.Join([]string{heading, "", s.table.View()}, "\n")
	}
	return strings.Join([]string{heading, "", s.table.View(), "", s.detail}, "\n")
}

func (s *listScreen) buildTable() {
	if len(s.routes) == 0 {
		empty := components.NewEmpty(s.theme, components.EmptyProps{
			Icon:        "↯",
			Title:       "No routes configured yet.",
			Description: "Create the first webhook entrypoint from this panel.",
			Hint:        "[n] new   [esc] back",
		})
		s.empty = &empty
		s.table = nil
		s.detail = ""
		return
	}
	rows := make([]btable.Row, 0, len(s.routes))
	for _, route := range s.routes {
		rows = append(rows, btable.Row{
			route.Path,
			string(route.Auth.Type),
			formatRouteAction(route.Action),
			timeoutSummary(route.Timeout),
		})
	}
	tb := components.NewTable(s.theme, []btable.Column{
		{Title: "Path", Width: 24},
		{Title: "Auth", Width: 12},
		{Title: "Action", Width: 32},
		{Title: "Timeout", Width: 10},
	}, rows)
	s.table = &tb
	s.empty = nil
	if route := s.selectedRoute(); route != nil {
		s.detail = routeDetail(s.theme, *route)
	}
}

func (s *listScreen) selectedRoute() *fairwayctl.Route {
	if s.table == nil || len(s.routes) == 0 {
		return nil
	}
	index := s.table.Cursor()
	if index < 0 || index >= len(s.routes) {
		return nil
	}
	clone := s.routes[index]
	return &clone
}
