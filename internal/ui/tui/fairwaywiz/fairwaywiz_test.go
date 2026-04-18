package fairwaywiz

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type fakeClient struct {
	routes      []fairwayctl.Route
	status      fairwayctl.StatusInfo
	added       []fairwayctl.Route
	deleted     []string
	addErr      error
	deleteErr   error
	statusErr   error
	listErr     error
	lastAdded   fairwayctl.Route
	lastDeleted string
}

func (f *fakeClient) RouteList(_ context.Context) ([]fairwayctl.Route, error) {
	return append([]fairwayctl.Route{}, f.routes...), f.listErr
}
func (f *fakeClient) RouteAdd(_ context.Context, route fairwayctl.Route) error {
	f.added = append(f.added, route)
	f.lastAdded = route
	if f.addErr != nil {
		return f.addErr
	}
	f.routes = append(f.routes, route)
	return nil
}
func (f *fakeClient) RouteDelete(_ context.Context, path string) error {
	f.deleted = append(f.deleted, path)
	f.lastDeleted = path
	if f.deleteErr != nil {
		return f.deleteErr
	}
	out := f.routes[:0]
	for _, route := range f.routes {
		if route.Path != path {
			out = append(out, route)
		}
	}
	f.routes = out
	return nil
}
func (f *fakeClient) Status(_ context.Context) (fairwayctl.StatusInfo, error) {
	return f.status, f.statusErr
}

func TestMenu_navigatesDown_highlightsItems(t *testing.T) {
	svc := &fakeClient{}
	root := NewRoot(svc)
	if !strings.Contains(root.View(), "Manage routes") {
		t.Fatalf("expected menu view, got %q", root.View())
	}
	_, _ = root.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !strings.Contains(root.View(), "View status") {
		t.Fatalf("expected status item visible, got %q", root.View())
	}
}

func TestMenu_selectManageRoutes_loadsList(t *testing.T) {
	svc := &fakeClient{routes: []fairwayctl.Route{{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}}}
	root := NewRoot(svc)
	_, cmd := root.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected load routes command")
	}
	_, _ = root.Update(cmd())
	if !strings.Contains(root.View(), "Routes (1)") {
		t.Fatalf("expected routes list, got %q", root.View())
	}
}

func TestList_showsRoutes(t *testing.T) {
	svc := &fakeClient{routes: []fairwayctl.Route{{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}, Timeout: 30}}}
	screen := newListScreen(theme.New(), svc)
	screen, _ = screen.Update(routesLoadedMsg{routes: svc.routes})
	view := screen.View()
	if !strings.Contains(view, "/hooks/github") || !strings.Contains(view, "cron.run:AB12CD") {
		t.Fatalf("unexpected list view: %q", view)
	}
}

func TestList_emptyState_showsHelp(t *testing.T) {
	svc := &fakeClient{}
	screen := newListScreen(theme.New(), svc)
	screen, _ = screen.Update(routesLoadedMsg{routes: nil})
	if !strings.Contains(screen.View(), "No routes configured yet.") {
		t.Fatalf("unexpected empty view: %q", screen.View())
	}
}

func TestForm_fillsAndSubmits_createsRoute(t *testing.T) {
	svc := &fakeClient{}
	screen := newFormScreen(theme.New(), svc, nil).(*formScreen)
	screen.path.SetValue("/hooks/github")
	screen.authSecret.SetValue("secret")
	screen.actionTgt.SetValue("AB12CD")
	screen.timeout.SetValue("30s")
	screen.focus = fieldSubmit
	screen.syncFocus()
	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen = next.(*formScreen)
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	next, _ = screen.Update(cmd())
	screen = next.(*formScreen)
	if len(svc.added) != 1 {
		t.Fatalf("expected one add, got %d", len(svc.added))
	}
	if svc.added[0].Path != "/hooks/github" {
		t.Fatalf("unexpected route: %+v", svc.added[0])
	}
	if !strings.Contains(screen.View(), "Route created successfully.") {
		t.Fatalf("unexpected success view: %q", screen.View())
	}
}

func TestForm_submitThenReturnToList_showsCreatedRoute(t *testing.T) {
	svc := &fakeClient{}
	screen := newFormScreen(theme.New(), svc, nil).(*formScreen)
	screen.path.SetValue("/hooks/telegram")
	screen.authSecret.SetValue("secret")
	screen.actionTgt.SetValue("AB12CD")
	screen.focus = fieldSubmit
	screen.syncFocus()

	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen = next.(*formScreen)
	next, _ = screen.Update(cmd())
	screen = next.(*formScreen)

	next, cmd = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if next.State() != stateList {
		t.Fatalf("expected list state, got %v", next.State())
	}
	if cmd == nil {
		t.Fatal("expected list reload command")
	}
	next, _ = next.Update(cmd())
	if !strings.Contains(next.View(), "/hooks/telegram") {
		t.Fatalf("expected created route in list, got %q", next.View())
	}
}

func TestForm_invalidPath_showsError_doesNotSubmit(t *testing.T) {
	svc := &fakeClient{}
	screen := newFormScreen(theme.New(), svc, nil).(*formScreen)
	screen.path.SetValue("invalid")
	screen.authSecret.SetValue("secret")
	screen.actionTgt.SetValue("AB12CD")
	screen.focus = fieldSubmit
	screen.syncFocus()
	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen = next.(*formScreen)
	if cmd != nil {
		t.Fatal("expected no submit command")
	}
	if len(svc.added) != 0 {
		t.Fatalf("expected no add call, got %d", len(svc.added))
	}
	if !strings.Contains(screen.View(), "path must start") {
		t.Fatalf("unexpected error view: %q", screen.View())
	}
}

func TestForm_duplicatePathFromServer_showsError_keepsFormOpen(t *testing.T) {
	svc := &fakeClient{addErr: fairwayctl.ErrDuplicatePath}
	screen := newFormScreen(theme.New(), svc, nil).(*formScreen)
	screen.path.SetValue("/hooks/github")
	screen.authSecret.SetValue("secret")
	screen.actionTgt.SetValue("AB12CD")
	screen.focus = fieldSubmit
	screen.syncFocus()
	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen = next.(*formScreen)
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	next, _ = screen.Update(cmd())
	screen = next.(*formScreen)
	if screen.done != nil {
		t.Fatal("expected form to remain open")
	}
	if !strings.Contains(screen.View(), "already exists") {
		t.Fatalf("unexpected duplicate error view: %q", screen.View())
	}
}

func TestForm_serverValidationError_showsError_keepsFormOpen(t *testing.T) {
	svc := &fakeClient{addErr: errors.New("bearer auth requires a non-empty token")}
	screen := newFormScreen(theme.New(), svc, nil).(*formScreen)
	screen.path.SetValue("/hooks/github")
	screen.authSecret.SetValue("secret")
	screen.actionTgt.SetValue("AB12CD")
	screen.focus = fieldSubmit
	screen.syncFocus()
	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen = next.(*formScreen)
	next, _ = screen.Update(cmd())
	screen = next.(*formScreen)
	if screen.done != nil {
		t.Fatal("expected form to remain open")
	}
	if !strings.Contains(screen.View(), "bearer auth requires") {
		t.Fatalf("unexpected server error view: %q", screen.View())
	}
}

func TestForm_editRoute_updatesRoute(t *testing.T) {
	original := fairwayctl.Route{
		Path:   "/hooks/github",
		Auth:   fairwayctl.Auth{Type: fairwayctl.AuthBearer, Token: "secret"},
		Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"},
	}
	svc := &fakeClient{routes: []fairwayctl.Route{original}}
	screen := newFormScreen(theme.New(), svc, &original).(*formScreen)
	screen.actionTgt.SetValue("ZX99")
	screen.focus = fieldSubmit
	screen.syncFocus()
	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen = next.(*formScreen)
	next, _ = screen.Update(cmd())
	screen = next.(*formScreen)
	if len(svc.deleted) != 1 || svc.deleted[0] != original.Path {
		t.Fatalf("expected old route deletion, got %+v", svc.deleted)
	}
	if len(svc.added) != 1 {
		t.Fatalf("expected delete/re-add sequence, got %+v", svc.added)
	}
	if !strings.Contains(screen.View(), "Route updated successfully.") {
		t.Fatalf("unexpected edit success view: %q", screen.View())
	}
}

func TestForm_esc_returnsToList_withoutSubmit(t *testing.T) {
	svc := &fakeClient{routes: []fairwayctl.Route{{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}}}
	screen := newFormScreen(theme.New(), svc, nil)
	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.State() != stateList {
		t.Fatalf("expected list state, got %v", next.State())
	}
	if cmd == nil {
		t.Fatal("expected reload command")
	}
}

func TestDelete_confirms_invokesRouteDelete(t *testing.T) {
	svc := &fakeClient{}
	route := fairwayctl.Route{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}
	screen := newDeleteScreen(theme.New(), svc, route).(*deleteScreen)
	next, _ := screen.Update(tea.KeyMsg{Type: tea.KeyLeft})
	screen = next.(*deleteScreen)
	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen = next.(*deleteScreen)
	if cmd == nil {
		t.Fatal("expected delete command")
	}
	next, _ = screen.Update(cmd())
	screen = next.(*deleteScreen)
	if len(svc.deleted) != 1 || svc.deleted[0] != route.Path {
		t.Fatalf("expected delete %q, got %+v", route.Path, svc.deleted)
	}
	if !strings.Contains(screen.View(), "Route deleted successfully.") {
		t.Fatalf("unexpected success view: %q", screen.View())
	}
}

func TestStatus_loadsDaemonPanel(t *testing.T) {
	svc := &fakeClient{
		status: fairwayctl.StatusInfo{Version: "0.18.0", Uptime: "4m", Bind: "127.0.0.1", Port: 4321, InFlight: 1, RouteCount: 1},
		routes: []fairwayctl.Route{{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}},
	}
	screen := newStatusScreen(theme.New(), svc)
	screen, _ = screen.Update(statusLoadedMsg{status: svc.status, routes: svc.routes})
	view := screen.View()
	if !strings.Contains(view, "Daemon") || !strings.Contains(view, "/hooks/github") {
		t.Fatalf("unexpected status view: %q", view)
	}
}

func TestDelete_esc_cancels(t *testing.T) {
	svc := &fakeClient{}
	screen := newDeleteScreen(theme.New(), svc, fairwayctl.Route{Path: "/hooks/github"})
	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.State() != stateList {
		t.Fatalf("expected list state, got %v", next.State())
	}
	if cmd == nil {
		t.Fatal("expected reload command")
	}
}

func TestRoot_snapshotScreens(t *testing.T) {
	svc := &fakeClient{routes: []fairwayctl.Route{{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}}}
	tm := teatest.NewTestModel(t, NewRoot(svc), teatest.WithInitialTermSize(100, 30))
	t.Cleanup(func() { _ = tm.Quit() })

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Fairway Config"))
	})

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Routes (1)"))
	}, teatest.WithDuration(3*time.Second))
}

func TestRoot_snapshotFormScreen(t *testing.T) {
	svc := &fakeClient{}
	tm := teatest.NewTestModel(t, NewRoot(svc), teatest.WithInitialTermSize(100, 30))
	t.Cleanup(func() { _ = tm.Quit() })

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Fairway Config"))
	})

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("No routes configured yet."))
	}, teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Step 1 of"))
	})
}

func TestRouteFromFormState_variants(t *testing.T) {
	tests := []struct {
		name  string
		state formState
		check func(t *testing.T, route fairwayctl.Route)
	}{
		{
			name: "bearer cron",
			state: formState{
				Path:         "/hooks/github",
				AuthType:     string(fairwayctl.AuthBearer),
				AuthSecret:   "secret",
				ActionType:   string(fairwayctl.ActionCronRun),
				ActionTarget: "AB12CD",
				Timeout:      "30s",
			},
			check: func(t *testing.T, route fairwayctl.Route) {
				if route.Auth.Token != "secret" || route.Action.Target != "AB12CD" {
					t.Fatalf("unexpected route: %+v", route)
				}
			},
		},
		{
			name: "token http forward",
			state: formState{
				Path:         "/hooks/forward",
				AuthType:     string(fairwayctl.AuthToken),
				AuthSecret:   "abc",
				AuthLookup:   "query:token",
				ActionType:   string(fairwayctl.ActionHTTPForward),
				ActionTarget: "https://example.com/inbox",
				ActionMeta:   "POST",
			},
			check: func(t *testing.T, route fairwayctl.Route) {
				if route.Auth.Query != "token" || route.Action.URL != "https://example.com/inbox" || route.Action.Method != "POST" {
					t.Fatalf("unexpected route: %+v", route)
				}
			},
		},
		{
			name: "local only telegram",
			state: formState{
				Path:       "/internal/events",
				AuthType:   string(fairwayctl.AuthLocalOnly),
				ActionType: string(fairwayctl.ActionTelegramHandle),
				ActionMeta: "telegram",
			},
			check: func(t *testing.T, route fairwayctl.Route) {
				if route.Auth.Type != fairwayctl.AuthLocalOnly || route.Action.Provider != "telegram" {
					t.Fatalf("unexpected route: %+v", route)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			route, err := routeFromFormState(tc.state)
			if err != nil {
				t.Fatalf("routeFromFormState() error = %v", err)
			}
			tc.check(t, route)
		})
	}
}

func TestRouteFromFormState_invalidTimeout(t *testing.T) {
	_, err := routeFromFormState(formState{
		Path:         "/hooks/github",
		AuthType:     string(fairwayctl.AuthBearer),
		AuthSecret:   "secret",
		ActionType:   string(fairwayctl.ActionCronRun),
		ActionTarget: "AB12CD",
		Timeout:      "later",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestForm_visibleFields_changeWithSelections(t *testing.T) {
	screen := newFormScreen(theme.New(), &fakeClient{}, nil).(*formScreen)
	if got := screen.visibleFields(); len(got) != 7 {
		t.Fatalf("unexpected default field count: %d", len(got))
	}

	screen.authType.SetSelectedByKey(string(fairwayctl.AuthToken))
	screen.actionType.SetSelectedByKey(string(fairwayctl.ActionHTTPForward))
	got := screen.visibleFields()
	if len(got) < 8 {
		t.Fatalf("expected token + http.forward fields, got %v", got)
	}
}

func TestFormStateFromRoute_roundTripEditPrefill(t *testing.T) {
	route := &fairwayctl.Route{
		Path: "/hooks/forward",
		Auth: fairwayctl.Auth{
			Type:  fairwayctl.AuthToken,
			Value: "abc",
			Query: "token",
		},
		Action: fairwayctl.Action{
			Type:   fairwayctl.ActionHTTPForward,
			URL:    "https://example.com/inbox",
			Method: "POST",
		},
		Timeout: 45 * time.Second,
	}
	state := formStateFromRoute(route)
	if state.Path != route.Path || state.AuthLookup != "query:token" || state.ActionTarget != route.Action.URL || state.ActionMeta != "POST" {
		t.Fatalf("unexpected prefill state: %+v", state)
	}
}

func TestHelpers_formatAndSummaries(t *testing.T) {
	if got := formatRouteAction(fairwayctl.Action{Type: fairwayctl.ActionHTTPForward, URL: "https://example.com"}); got != "http.forward:https://example.com" {
		t.Fatalf("formatRouteAction(url) = %q", got)
	}
	if got := authSummary(fairwayctl.Auth{Type: fairwayctl.AuthToken, Header: "X-Test"}); got != "token header=X-Test" {
		t.Fatalf("authSummary(token) = %q", got)
	}
	if got := timeoutSummary(0); got != "default" {
		t.Fatalf("timeoutSummary(0) = %q", got)
	}
	if got := blankOr(""); got != "(empty)" {
		t.Fatalf("blankOr(empty) = %q", got)
	}
}

func TestList_hotkeys_openEditAndDelete(t *testing.T) {
	route := fairwayctl.Route{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}
	svc := &fakeClient{routes: []fairwayctl.Route{route}}
	screen := newListScreen(theme.New(), svc)
	screen, _ = screen.Update(routesLoadedMsg{routes: svc.routes})

	next, _ := screen.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if next.State() != stateForm {
		t.Fatalf("expected form state on edit, got %v", next.State())
	}

	screen = newListScreen(theme.New(), svc)
	screen, _ = screen.Update(routesLoadedMsg{routes: svc.routes})
	next, _ = screen.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if next.State() != stateDelete {
		t.Fatalf("expected delete state on delete, got %v", next.State())
	}
}

func TestStatus_refreshAndError(t *testing.T) {
	svc := &fakeClient{statusErr: errors.New("daemon offline")}
	screen := newStatusScreen(theme.New(), svc)
	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("expected refresh command")
	}
	next, _ = next.Update(cmd())
	if !strings.Contains(next.View(), "daemon offline") {
		t.Fatalf("unexpected error view: %q", next.View())
	}
}

func TestSummaryForScreen(t *testing.T) {
	form := newFormScreen(theme.New(), &fakeClient{}, nil).(*formScreen)
	form.done = &fairwayctl.Route{Path: "/hooks/github"}
	if got := summaryForScreen(form); got != "Created route /hooks/github" {
		t.Fatalf("unexpected create summary: %q", got)
	}

	del := newDeleteScreen(theme.New(), &fakeClient{}, fairwayctl.Route{Path: "/hooks/github"}).(*deleteScreen)
	del.donePath = "/hooks/github"
	if got := summaryForScreen(del); got != "Deleted route /hooks/github" {
		t.Fatalf("unexpected delete summary: %q", got)
	}
}

func TestSharedCommandsAndHelpers(t *testing.T) {
	route := fairwayctl.Route{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer, Token: "secret"}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}

	svc := &fakeClient{routes: []fairwayctl.Route{route}, status: fairwayctl.StatusInfo{Version: "0.18.0"}}
	if msg := loadRoutesCmd(svc)().(routesLoadedMsg); len(msg.routes) != 1 || msg.err != nil {
		t.Fatalf("unexpected routes msg: %+v", msg)
	}
	if msg := loadStatusCmd(svc)().(statusLoadedMsg); msg.status.Version != "0.18.0" || len(msg.routes) != 1 {
		t.Fatalf("unexpected status msg: %+v", msg)
	}
	if got := routeToMenuItems([]fairwayctl.Route{route}); len(got) != 1 || got[0].Key != route.Path {
		t.Fatalf("unexpected route menu items: %+v", got)
	}
	if found := findRoute([]fairwayctl.Route{route}, route.Path); found == nil || found.Path != route.Path {
		t.Fatalf("expected route lookup")
	}
	if got := humanizeRouteError(fairwayctl.ErrRouteNotFound, route.Path); !strings.Contains(got, "no longer exists") {
		t.Fatalf("unexpected humanized error: %q", got)
	}
}

func TestSubmitRouteCmd_branches(t *testing.T) {
	original := fairwayctl.Route{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer, Token: "secret"}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}
	updated := original
	updated.Action.Target = "ZX99"

	svc := &fakeClient{routes: []fairwayctl.Route{original}}
	if msg := submitRouteCmd(svc, nil, original)().(routeSubmitMsg); msg.err != nil || len(svc.added) != 1 {
		t.Fatalf("unexpected create submit msg: %+v added=%d", msg, len(svc.added))
	}

	svc = &fakeClient{routes: []fairwayctl.Route{original}}
	if msg := submitRouteCmd(svc, &original, updated)().(routeSubmitMsg); msg.err != nil || len(svc.deleted) != 1 || len(svc.added) != 1 {
		t.Fatalf("unexpected edit same-path msg: %+v added=%d deleted=%d", msg, len(svc.added), len(svc.deleted))
	}

	renamed := updated
	renamed.Path = "/hooks/github/v2"
	svc = &fakeClient{routes: []fairwayctl.Route{original}}
	if msg := submitRouteCmd(svc, &original, renamed)().(routeSubmitMsg); msg.err != nil || len(svc.deleted) != 1 || len(svc.added) != 1 {
		t.Fatalf("unexpected edit renamed msg: %+v added=%d deleted=%d", msg, len(svc.added), len(svc.deleted))
	}

	svc = &fakeClient{routes: []fairwayctl.Route{original}, addErr: fairwayctl.ErrDuplicatePath}
	if msg := submitRouteCmd(svc, &original, updated)().(routeSubmitMsg); msg.err == nil || len(svc.added) < 1 {
		t.Fatalf("expected failing edit same-path msg: %+v added=%d", msg, len(svc.added))
	}

	svc = &fakeClient{routes: []fairwayctl.Route{original}, deleteErr: fairwayctl.ErrRouteNotFound}
	if msg := submitRouteCmd(svc, &original, renamed)().(routeSubmitMsg); msg.err == nil {
		t.Fatalf("expected failing rename msg")
	}
}

func TestDelete_errorAndDoneEnter(t *testing.T) {
	route := fairwayctl.Route{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}
	svc := &fakeClient{deleteErr: fairwayctl.ErrRouteNotFound}
	screen := newDeleteScreen(theme.New(), svc, route).(*deleteScreen)
	next, _ := screen.Update(tea.KeyMsg{Type: tea.KeyLeft})
	screen = next.(*deleteScreen)
	next, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen = next.(*deleteScreen)
	next, _ = screen.Update(cmd())
	screen = next.(*deleteScreen)
	if !strings.Contains(screen.View(), "no longer exists") {
		t.Fatalf("unexpected delete error view: %q", screen.View())
	}

	svc = &fakeClient{}
	screen = newDeleteScreen(theme.New(), svc, route).(*deleteScreen)
	next, _ = screen.Update(tea.KeyMsg{Type: tea.KeyLeft})
	screen = next.(*deleteScreen)
	next, cmd = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen = next.(*deleteScreen)
	next, _ = screen.Update(cmd())
	screen = next.(*deleteScreen)
	next, cmd = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if next.State() != stateList || cmd == nil {
		t.Fatalf("expected return to list after delete completion")
	}
}

func TestList_loadingErrorEnterAndEsc(t *testing.T) {
	svc := &fakeClient{}
	screen := newListScreen(theme.New(), svc).(*listScreen)
	if !strings.Contains(screen.View(), "Loading routes") {
		t.Fatalf("unexpected loading view: %q", screen.View())
	}
	next, _ := screen.Update(routesLoadedMsg{err: errors.New("boom")})
	screen = next.(*listScreen)
	if !strings.Contains(screen.View(), "boom") {
		t.Fatalf("unexpected error view: %q", screen.View())
	}

	route := fairwayctl.Route{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}
	screen = newListScreen(theme.New(), svc).(*listScreen)
	next, _ = screen.Update(routesLoadedMsg{routes: []fairwayctl.Route{route}})
	screen = next.(*listScreen)
	next, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen = next.(*listScreen)
	if !strings.Contains(screen.View(), "Selected route") {
		t.Fatalf("unexpected detail view: %q", screen.View())
	}
	next, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.State() != stateMenu {
		t.Fatalf("expected menu state, got %v", next.State())
	}
}

func TestForm_metadataNavigationAndSections(t *testing.T) {
	route := &fairwayctl.Route{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer, Token: "secret"}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}
	screen := newFormScreen(theme.New(), &fakeClient{}, route).(*formScreen)
	if screen.Title() != "Edit Route" {
		t.Fatalf("unexpected title: %q", screen.Title())
	}
	if got := strings.Join(screen.Breadcrumb(), "/"); !strings.Contains(got, "edit-route") {
		t.Fatalf("unexpected breadcrumb: %q", got)
	}

	_, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	screen.focus = fieldPath
	next, _ := screen.Update(tea.KeyMsg{Type: tea.KeyTab})
	screen = next.(*formScreen)
	if screen.focus == fieldPath {
		t.Fatal("expected focus advance on tab")
	}
	next, _ = screen.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	screen = next.(*formScreen)
	if screen.focus != fieldPath {
		t.Fatalf("expected focus back to path, got %v", screen.focus)
	}

	screen.authType.SetSelectedByKey(string(fairwayctl.AuthLocalOnly))
	if !strings.Contains(screen.authSection(), "Local-only routes") {
		t.Fatalf("unexpected local auth section")
	}
	screen.actionType.SetSelectedByKey(string(fairwayctl.ActionHTTPForward))
	if !strings.Contains(screen.actionSection(), "Forward URL") {
		t.Fatalf("unexpected forward action section")
	}
	if !strings.Contains(screen.submitPanel(), "save route") {
		t.Fatalf("unexpected submit panel: %q", screen.submitPanel())
	}
}
