package fairwaywiz

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type state int

const (
	stateMenu state = iota
	stateList
	stateForm
	stateDelete
	stateStatus
)

type FairwayClient interface {
	RouteList(ctx context.Context) ([]fairwayctl.Route, error)
	RouteAdd(ctx context.Context, route fairwayctl.Route) error
	RouteDelete(ctx context.Context, path string) error
	Status(ctx context.Context) (fairwayctl.StatusInfo, error)
}

type Screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Screen, tea.Cmd)
	View() string
	Title() string
	Breadcrumb() []string
	Footer() []components.KeyHint
	State() state
}

type mode int

const (
	modeCreate mode = iota
	modeEdit
)

var (
	authOptions = []components.MenuItem{
		{Title: "Bearer", Description: "Shared secret in Authorization header.", Key: string(fairwayctl.AuthBearer)},
		{Title: "Token", Description: "Custom token from header or query string.", Key: string(fairwayctl.AuthToken)},
		{Title: "Local only", Description: "Accept only internal requests without token.", Key: string(fairwayctl.AuthLocalOnly)},
	}
	actionOptions = []components.MenuItem{
		{Title: "cron.run", Description: "Trigger one cron job by ID.", Key: string(fairwayctl.ActionCronRun)},
		{Title: "crew.run", Description: "Run a crew agent by name (shipyard-crew addon).", Key: string(fairwayctl.ActionCrewRun)},
		{Title: "cron.enable", Description: "Enable one cron job.", Key: string(fairwayctl.ActionCronEnable)},
		{Title: "cron.disable", Description: "Disable one cron job.", Key: string(fairwayctl.ActionCronDisable)},
		{Title: "service.start", Description: "Start a managed Shipyard service.", Key: string(fairwayctl.ActionServiceStart)},
		{Title: "service.stop", Description: "Stop a managed Shipyard service.", Key: string(fairwayctl.ActionServiceStop)},
		{Title: "service.restart", Description: "Restart a managed Shipyard service.", Key: string(fairwayctl.ActionServiceRestart)},
		{Title: "message.send", Description: "Dispatch a message via the configured provider.", Key: string(fairwayctl.ActionMessageSend)},
		{Title: "telegram.handle", Description: "Forward to the Telegram handler.", Key: string(fairwayctl.ActionTelegramHandle)},
		{Title: "http.forward", Description: "Proxy the webhook to an HTTP endpoint.", Key: string(fairwayctl.ActionHTTPForward)},
	}
)

func loadRoutesCmd(client FairwayClient) tea.Cmd {
	return func() tea.Msg {
		routes, err := client.RouteList(context.Background())
		return routesLoadedMsg{routes: routes, err: err}
	}
}

func loadStatusCmd(client FairwayClient) tea.Cmd {
	return func() tea.Msg {
		status, err := client.Status(context.Background())
		if err != nil {
			return statusLoadedMsg{err: err}
		}
		routes, err := client.RouteList(context.Background())
		return statusLoadedMsg{status: status, routes: routes, err: err}
	}
}

func submitRouteCmd(client FairwayClient, original *fairwayctl.Route, route fairwayctl.Route) tea.Cmd {
	return func() tea.Msg {
		if original == nil {
			err := client.RouteAdd(context.Background(), route)
			return routeSubmitMsg{route: route, err: err}
		}

		if original.Path == route.Path {
			if err := client.RouteDelete(context.Background(), original.Path); err != nil {
				return routeSubmitMsg{route: route, err: err}
			}
			if err := client.RouteAdd(context.Background(), route); err != nil {
				_ = client.RouteAdd(context.Background(), *original)
				return routeSubmitMsg{route: route, err: err}
			}
			return routeSubmitMsg{route: route}
		}

		if err := client.RouteAdd(context.Background(), route); err != nil {
			return routeSubmitMsg{route: route, err: err}
		}
		if err := client.RouteDelete(context.Background(), original.Path); err != nil {
			_ = client.RouteDelete(context.Background(), route.Path)
			return routeSubmitMsg{route: route, err: err}
		}
		return routeSubmitMsg{route: route}
	}
}

func deleteRouteCmd(client FairwayClient, path string) tea.Cmd {
	return func() tea.Msg {
		err := client.RouteDelete(context.Background(), path)
		return routeDeleteMsg{path: path, err: err}
	}
}

func routeToMenuItems(routes []fairwayctl.Route) []components.MenuItem {
	items := make([]components.MenuItem, 0, len(routes))
	for _, route := range routes {
		timeout := "default"
		if route.Timeout > 0 {
			timeout = route.Timeout.String()
		}
		items = append(items, components.MenuItem{
			Title:       route.Path,
			Description: fmt.Sprintf("%s  %s  %s", route.Auth.Type, formatRouteAction(route.Action), timeout),
			Key:         route.Path,
		})
	}
	return items
}

func findRoute(routes []fairwayctl.Route, path string) *fairwayctl.Route {
	for i := range routes {
		if routes[i].Path == path {
			clone := routes[i]
			return &clone
		}
	}
	return nil
}

func formatRouteAction(action fairwayctl.Action) string {
	switch {
	case action.Target != "":
		return string(action.Type) + ":" + action.Target
	case action.URL != "":
		return string(action.Type) + ":" + action.URL
	case action.Provider != "":
		return string(action.Type) + ":" + action.Provider
	default:
		return string(action.Type)
	}
}

func routeDetail(th theme.Theme, route fairwayctl.Route) string {
	lines := [][2]string{
		{"Path", route.Path},
		{"Auth", authSummary(route.Auth)},
		{"Action", formatRouteAction(route.Action)},
		{"Timeout", timeoutSummary(route.Timeout)},
	}
	return renderReview(th, "Selected route", lines)
}

func renderReview(th theme.Theme, title string, lines [][2]string) string {
	out := []string{th.ValueStyle.Render(title)}
	for _, line := range lines {
		out = append(out, th.LabelStyle.Render(line[0]+":")+" "+th.ValueStyle.Render(line[1]))
	}
	return strings.Join(out, "\n")
}

func authSummary(auth fairwayctl.Auth) string {
	switch auth.Type {
	case fairwayctl.AuthBearer:
		if auth.Token != "" {
			return "bearer token"
		}
	case fairwayctl.AuthToken:
		parts := []string{"token"}
		if auth.Header != "" {
			parts = append(parts, "header="+auth.Header)
		}
		if auth.Query != "" {
			parts = append(parts, "query="+auth.Query)
		}
		return strings.Join(parts, " ")
	case fairwayctl.AuthLocalOnly:
		return "local-only"
	}
	return string(auth.Type)
}

func timeoutSummary(d time.Duration) string {
	if d <= 0 {
		return "default"
	}
	return d.String()
}

func humanizeRouteError(err error, path string) string {
	switch {
	case errors.Is(err, fairwayctl.ErrDuplicatePath):
		return fmt.Sprintf("route %q already exists; choose another path or edit the existing route", path)
	case errors.Is(err, fairwayctl.ErrRouteNotFound):
		return fmt.Sprintf("route %q no longer exists; refresh the list and try again", path)
	default:
		return err.Error()
	}
}

func routeFromFormState(s formState) (fairwayctl.Route, error) {
	route := fairwayctl.Route{
		Path: s.Path,
		Auth: fairwayctl.Auth{Type: fairwayctl.AuthType(s.AuthType)},
		Action: fairwayctl.Action{
			Type: fairwayctl.ActionType(s.ActionType),
		},
	}

	switch route.Auth.Type {
	case fairwayctl.AuthBearer:
		route.Auth.Token = strings.TrimSpace(s.AuthSecret)
	case fairwayctl.AuthToken:
		route.Auth.Value = strings.TrimSpace(s.AuthSecret)
		lookup := strings.TrimSpace(s.AuthLookup)
		if lookup != "" {
			if strings.HasPrefix(strings.ToLower(lookup), "query:") {
				route.Auth.Query = strings.TrimSpace(strings.TrimPrefix(lookup, "query:"))
			} else {
				route.Auth.Header = lookup
			}
		}
	case fairwayctl.AuthLocalOnly:
	}

	switch route.Action.Type {
	case fairwayctl.ActionCronRun, fairwayctl.ActionCrewRun, fairwayctl.ActionCronEnable, fairwayctl.ActionCronDisable,
		fairwayctl.ActionServiceStart, fairwayctl.ActionServiceStop, fairwayctl.ActionServiceRestart:
		route.Action.Target = strings.TrimSpace(s.ActionTarget)
	case fairwayctl.ActionMessageSend:
		route.Action.Provider = strings.TrimSpace(s.ActionMeta)
		route.Action.Target = strings.TrimSpace(s.ActionTarget)
	case fairwayctl.ActionTelegramHandle:
		route.Action.Provider = strings.TrimSpace(s.ActionMeta)
	case fairwayctl.ActionHTTPForward:
		route.Action.URL = strings.TrimSpace(s.ActionTarget)
		route.Action.Method = strings.TrimSpace(s.ActionMeta)
	}

	if raw := strings.TrimSpace(s.Timeout); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return fairwayctl.Route{}, fmt.Errorf("invalid timeout: %w", err)
		}
		route.Timeout = timeout
	}

	if err := route.Validate(); err != nil {
		return fairwayctl.Route{}, err
	}
	return route, nil
}

type formState struct {
	Path         string
	AuthType     string
	AuthSecret   string
	AuthLookup   string
	ActionType   string
	ActionTarget string
	ActionMeta   string
	Timeout      string
}

func formStateFromRoute(route *fairwayctl.Route) formState {
	if route == nil {
		return formState{
			AuthType:   string(fairwayctl.AuthBearer),
			ActionType: string(fairwayctl.ActionCronRun),
			Timeout:    "30s",
		}
	}
	state := formState{
		Path:         route.Path,
		AuthType:     string(route.Auth.Type),
		ActionType:   string(route.Action.Type),
		ActionTarget: route.Action.Target,
		ActionMeta:   route.Action.Provider,
		Timeout:      timeoutSummary(route.Timeout),
	}
	switch route.Auth.Type {
	case fairwayctl.AuthBearer:
		state.AuthSecret = route.Auth.Token
	case fairwayctl.AuthToken:
		state.AuthSecret = route.Auth.Value
		if route.Auth.Query != "" {
			state.AuthLookup = "query:" + route.Auth.Query
		} else {
			state.AuthLookup = route.Auth.Header
		}
	}
	switch route.Action.Type {
	case fairwayctl.ActionHTTPForward:
		state.ActionTarget = route.Action.URL
		state.ActionMeta = route.Action.Method
	case fairwayctl.ActionMessageSend:
		state.ActionMeta = route.Action.Provider
	case fairwayctl.ActionTelegramHandle:
		state.ActionMeta = route.Action.Provider
	}
	if route.Timeout == 0 {
		state.Timeout = ""
	}
	return state
}
