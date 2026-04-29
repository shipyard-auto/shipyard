package fairwaywiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/addon"
	"github.com/shipyard-auto/shipyard/internal/crewctl"
	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type formField int

const (
	fieldPath formField = iota
	fieldAuthType
	fieldAuthSecret
	fieldAuthLookup
	fieldActionType
	fieldActionTarget
	fieldActionMeta
	fieldTimeout
	fieldAsync
	fieldSubmit
)

type formScreen struct {
	theme      theme.Theme
	client     FairwayClient
	mode       mode
	original   *fairwayctl.Route
	path       components.Input
	authType   components.Menu
	authSecret components.Input
	authLookup components.Input
	actionType components.Menu
	actionTgt  components.Input
	crewMenu   components.Menu     // populado lazy quando action = crew.run
	crewAgents []crewctl.AgentInfo // empty se crew ausente OU sem agentes
	crewAddon  addon.Info          // resultado da última Detect
	crewLoaded bool                // garante que carregamos só uma vez
	actionMeta components.Input
	timeout    components.Input
	async      components.Menu
	focus      formField
	err        string
	submitting bool
	done       *fairwayctl.Route
}

func newFormScreen(th theme.Theme, client FairwayClient, route *fairwayctl.Route) Screen {
	state := formStateFromRoute(route)
	path := components.NewInput(th, "Path", "/hooks/github", nil)
	path.SetHint("Must start with / and cannot contain spaces, *, ?, or #.")
	path.SetValue(state.Path)

	authSecret := components.NewInput(th, "Token / secret", "secret", nil)
	authLookup := components.NewInput(th, "Header or query", "X-Webhook-Token or query:token", nil)
	actionTarget := components.NewInput(th, "Target / URL", "job-id or https://example.com/webhook", nil)
	actionMeta := components.NewInput(th, "Method / provider", "POST or telegram", nil)
	timeout := components.NewInput(th, "Timeout", "30s", nil)
	timeout.SetHint("Optional. Leave empty to inherit the daemon default.")
	timeout.SetValue(state.Timeout)

	crewAddon := detectCrewAddon()
	allowCrewRun := route != nil && route.Action.Type == fairwayctl.ActionCrewRun

	authMenu := components.NewMenu(th, authOptions)
	authMenu.SetSelectedByKey(state.AuthType)
	actionMenu := components.NewMenu(th, buildActionOptions(crewAddon.Installed, allowCrewRun))
	actionMenu.SetSelectedByKey(state.ActionType)
	asyncMenu := components.NewMenu(th, asyncOptions)
	asyncKey := "sync"
	if state.Async {
		asyncKey = "async"
	}
	asyncMenu.SetSelectedByKey(asyncKey)

	authSecret.SetValue(state.AuthSecret)
	authLookup.SetValue(state.AuthLookup)
	actionTarget.SetValue(state.ActionTarget)
	actionMeta.SetValue(state.ActionMeta)

	path.Focus()
	authSecret.Blur()
	authLookup.Blur()
	actionTarget.Blur()
	actionMeta.Blur()
	timeout.Blur()

	screen := &formScreen{
		theme:      th,
		client:     client,
		mode:       modeCreate,
		original:   route,
		path:       path,
		authType:   authMenu,
		authSecret: authSecret,
		authLookup: authLookup,
		actionType: actionMenu,
		actionTgt:  actionTarget,
		crewAddon:  crewAddon,
		actionMeta: actionMeta,
		timeout:    timeout,
		async:      asyncMenu,
	}
	if route != nil {
		screen.mode = modeEdit
	}
	screen.syncFocus()
	return screen
}

func (s *formScreen) Init() tea.Cmd { return s.path.Init() }
func (s *formScreen) Title() string {
	if s.mode == modeEdit {
		return "Edit Route"
	}
	return "New Route"
}
func (s *formScreen) Breadcrumb() []string {
	return []string{"fairway", "config", "routes", strings.ToLower(strings.ReplaceAll(s.Title(), " ", "-"))}
}
func (s *formScreen) Footer() []components.KeyHint {
	if s.done != nil {
		return []components.KeyHint{{Key: "enter", Label: "back to routes"}}
	}
	if s.submitting {
		return []components.KeyHint{{Key: "esc", Label: "cancel submit"}}
	}
	return []components.KeyHint{{Key: "enter", Label: "next"}, {Key: "shift+tab", Label: "back"}, {Key: "↑↓", Label: "choose"}, {Key: "esc", Label: "cancel"}}
}
func (s *formScreen) State() state { return stateForm }

func (s *formScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case routeSubmitMsg:
		s.submitting = false
		if msg.err != nil {
			s.err = humanizeRouteError(msg.err, msg.route.Path)
			return s, nil
		}
		s.done = &msg.route
		s.err = ""
		return s, nil
	case tea.WindowSizeMsg:
		for _, input := range []*components.Input{&s.path, &s.authSecret, &s.authLookup, &s.actionTgt, &s.actionMeta, &s.timeout} {
			input.Resize(msg.Width, msg.Height)
		}
		menu := s.authType.SetWidth(msg.Width)
		s.authType = menu
		action := s.actionType.SetWidth(msg.Width)
		s.actionType = action
		asyncSized := s.async.SetWidth(msg.Width)
		s.async = asyncSized
		if s.crewLoaded && len(s.crewAgents) > 0 {
			crewSized := s.crewMenu.SetWidth(msg.Width)
			s.crewMenu = crewSized
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return newListScreen(s.theme, s.client), loadRoutesCmd(s.client)
		case "tab":
			if s.done == nil && s.focus != fieldSubmit {
				s.focus = s.nextField()
				s.syncFocus()
			}
			return s, nil
		case "shift+tab":
			if s.done == nil {
				s.focus = s.prevField()
				s.syncFocus()
			}
			return s, nil
		case "enter":
			if s.done != nil {
				return newListScreen(s.theme, s.client), loadRoutesCmd(s.client)
			}
			if s.isTextField(s.focus) {
				s.err = ""
				s.focus = s.nextField()
				s.syncFocus()
				return s, nil
			}
		}
	}
	if s.done != nil {
		return s, nil
	}

	switch s.focus {
	case fieldPath:
		cmd, _ := s.path.Update(msg)
		return s, cmd
	case fieldAuthType:
		menu, cmd := s.authType.Update(msg)
		s.authType = menu
		if cmd != nil {
			s.err = ""
			s.focus = s.nextField()
			s.syncFocus()
		}
		return s, nil
	case fieldAuthSecret:
		cmd, _ := s.authSecret.Update(msg)
		return s, cmd
	case fieldAuthLookup:
		cmd, _ := s.authLookup.Update(msg)
		return s, cmd
	case fieldActionType:
		menu, cmd := s.actionType.Update(msg)
		s.actionType = menu
		if cmd != nil {
			s.err = ""
			s.focus = s.nextField()
			s.syncFocus()
		}
		return s, nil
	case fieldActionTarget:
		if s.crewPickerActive() {
			menu, cmd := s.crewMenu.Update(msg)
			s.crewMenu = menu
			if cmd != nil {
				// Enter no menu sinaliza seleção: copia a key para actionTgt
				// para que routeFromFormState (que lê s.actionTgt) capture o
				// valor correto, e avança o passo.
				s.actionTgt.SetValue(s.crewMenu.Selected().Key)
				s.err = ""
				s.focus = s.nextField()
				s.syncFocus()
			}
			return s, nil
		}
		cmd, _ := s.actionTgt.Update(msg)
		return s, cmd
	case fieldActionMeta:
		cmd, _ := s.actionMeta.Update(msg)
		return s, cmd
	case fieldTimeout:
		cmd, _ := s.timeout.Update(msg)
		return s, cmd
	case fieldAsync:
		menu, cmd := s.async.Update(msg)
		s.async = menu
		if cmd != nil {
			s.err = ""
			s.focus = s.nextField()
			s.syncFocus()
		}
		return s, nil
	case fieldSubmit:
		if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
			route, err := routeFromFormState(s.snapshot())
			if err != nil {
				s.err = err.Error()
				return s, nil
			}
			s.err = ""
			s.submitting = true
			return s, submitRouteCmd(s.client, s.original, route)
		}
	}
	return s, nil
}

func (s *formScreen) View() string {
	if s.done != nil {
		headline := "Route created successfully."
		if s.mode == modeEdit {
			headline = "Route updated successfully."
		}
		return s.theme.RenderSuccess(headline) + "\n\n" + routeDetail(s.theme, *s.done)
	}

	fields := s.visibleFields()
	stepIdx := 0
	for i, f := range fields {
		if f == s.focus {
			stepIdx = i
			break
		}
	}

	parts := []string{
		s.theme.SubtitleStyle.Render(fmt.Sprintf("Step %d of %d — %s", stepIdx+1, len(fields), s.stepLabel(s.focus))),
	}
	if summary := s.priorSummary(fields[:stepIdx]); summary != "" {
		parts = append(parts, summary)
	}
	parts = append(parts, s.renderCurrentStep())
	if s.err != "" {
		parts = append(parts, s.theme.RenderError(s.err))
	}
	return strings.Join(parts, "\n\n")
}

// loadCrewAgentsOnce popula s.crewMenu e s.crewAgents na primeira chamada.
// Idempotente: chamadas subsequentes são no-op. Não falha o wizard quando o
// crew está ausente ou listing falha — o caller cai no fallback do Input.
//
// Pré-condição: s.crewAddon já foi populado em newFormScreen (ver Passo 3-bis).
func (s *formScreen) loadCrewAgentsOnce() {
	if s.crewLoaded {
		return
	}
	s.crewLoaded = true

	if !s.crewAddon.Installed {
		return
	}

	agents, err := listCrewAgents()
	if err != nil || len(agents) == 0 {
		return
	}
	s.crewAgents = agents

	items := make([]components.MenuItem, 0, len(agents))
	for _, a := range agents {
		desc := a.Description
		if desc == "" {
			desc = "backend=" + a.Backend
		}
		items = append(items, components.MenuItem{
			Title:       a.Name,
			Description: desc,
			Key:         a.Name,
		})
	}
	s.crewMenu = components.NewMenu(s.theme, items)

	// Preserva seleção quando editando rota existente que aponta para um
	// agente conhecido. SetSelectedByKey é no-op quando a key não existe.
	if s.original != nil &&
		s.original.Action.Type == fairwayctl.ActionCrewRun &&
		s.original.Action.Target != "" {
		s.crewMenu.SetSelectedByKey(s.original.Action.Target)
	}
}

// crewPickerActive retorna true quando o step ActionTarget deve renderizar o
// menu de crew agents em vez do Input. Exige (a) action == crew.run, (b) crew
// instalado, (c) lista não-vazia.
func (s *formScreen) crewPickerActive() bool {
	if fairwayctl.ActionType(s.actionType.Selected().Key) != fairwayctl.ActionCrewRun {
		return false
	}
	return s.crewLoaded && len(s.crewAgents) > 0
}

// crewFallbackHint devolve a mensagem mostrada no Input quando o picker não
// está ativo (crew ausente OU sem agentes). Pré-condição: action == crew.run.
func (s *formScreen) crewFallbackHint() string {
	if !s.crewAddon.Installed {
		return "Install with `shipyard crew install` to list agents. You can still type a name."
	}
	return "No crew agents found. Run `shipyard crew hire <name>` first, or type a name."
}

func (s *formScreen) renderCurrentStep() string {
	switch s.focus {
	case fieldPath:
		return s.path.View()
	case fieldAuthType:
		return s.authType.View()
	case fieldAuthSecret:
		if fairwayctl.AuthType(s.authType.Selected().Key) == fairwayctl.AuthToken {
			s.authSecret.SetHint("Expected token value.")
		} else {
			s.authSecret.SetHint("Shared bearer secret expected in Authorization.")
		}
		return s.authSecret.View()
	case fieldAuthLookup:
		s.authLookup.SetHint("Use header name or query:parameter.")
		return s.authLookup.View()
	case fieldActionType:
		return s.actionType.View()
	case fieldActionTarget:
		switch fairwayctl.ActionType(s.actionType.Selected().Key) {
		case fairwayctl.ActionMessageSend:
			s.actionTgt.SetHint("Optional logical target for the message.")
			return s.actionTgt.View()
		case fairwayctl.ActionHTTPForward:
			s.actionTgt.SetHint("Destination URL starting with http:// or https://.")
			return s.actionTgt.View()
		case fairwayctl.ActionCrewRun:
			s.loadCrewAgentsOnce()
			if s.crewPickerActive() {
				return s.crewMenu.View()
			}
			s.actionTgt.SetHint(s.crewFallbackHint())
			return s.actionTgt.View()
		default:
			s.actionTgt.SetHint("Shipyard object ID or target name.")
			return s.actionTgt.View()
		}
	case fieldActionMeta:
		if fairwayctl.ActionType(s.actionType.Selected().Key) == fairwayctl.ActionHTTPForward {
			s.actionMeta.SetHint("Optional HTTP method override, for example POST.")
		} else {
			s.actionMeta.SetHint("Optional provider override.")
		}
		return s.actionMeta.View()
	case fieldTimeout:
		return s.timeout.View()
	case fieldAsync:
		if fairwayctl.ActionType(s.actionType.Selected().Key) == fairwayctl.ActionHTTPForward {
			return s.async.View() + "\n\n" + s.theme.RenderHint(
				"Note: http.forward routes cannot be async — selecting Async will fail at submit.",
			)
		}
		return s.async.View()
	case fieldSubmit:
		return s.submitPanel()
	}
	return ""
}

func (s *formScreen) stepLabel(f formField) string {
	switch f {
	case fieldPath:
		return "Path"
	case fieldAuthType:
		return "Auth type"
	case fieldAuthSecret:
		if fairwayctl.AuthType(s.authType.Selected().Key) == fairwayctl.AuthToken {
			return "Token value"
		}
		return "Bearer secret"
	case fieldAuthLookup:
		return "Header or query"
	case fieldActionType:
		return "Action"
	case fieldActionTarget:
		switch fairwayctl.ActionType(s.actionType.Selected().Key) {
		case fairwayctl.ActionMessageSend:
			return "Message target"
		case fairwayctl.ActionHTTPForward:
			return "Forward URL"
		case fairwayctl.ActionCrewRun:
			return "Crew agent"
		default:
			return "Action target"
		}
	case fieldActionMeta:
		if fairwayctl.ActionType(s.actionType.Selected().Key) == fairwayctl.ActionHTTPForward {
			return "Method"
		}
		return "Provider"
	case fieldTimeout:
		return "Timeout"
	case fieldAsync:
		return "Mode"
	case fieldSubmit:
		return "Review & confirm"
	}
	return ""
}

func (s *formScreen) priorSummary(done []formField) string {
	if len(done) == 0 {
		return ""
	}
	lines := make([]string, 0, len(done))
	for _, f := range done {
		value := s.fieldValue(f)
		if value == "" {
			value = s.theme.RenderHint("(empty)")
		} else {
			value = s.theme.ValueStyle.Render(value)
		}
		lines = append(lines, s.theme.LabelStyle.Render(s.stepLabel(f)+":")+" "+value)
	}
	return s.theme.PanelStyle.Render(strings.Join(lines, "\n"))
}

func (s *formScreen) fieldValue(f formField) string {
	switch f {
	case fieldPath:
		return strings.TrimSpace(s.path.Value())
	case fieldAuthType:
		return s.authType.Selected().Title
	case fieldAuthSecret:
		if strings.TrimSpace(s.authSecret.Value()) == "" {
			return ""
		}
		return "••••••••"
	case fieldAuthLookup:
		return strings.TrimSpace(s.authLookup.Value())
	case fieldActionType:
		return s.actionType.Selected().Title
	case fieldActionTarget:
		return strings.TrimSpace(s.actionTgt.Value())
	case fieldActionMeta:
		return strings.TrimSpace(s.actionMeta.Value())
	case fieldTimeout:
		return strings.TrimSpace(s.timeout.Value())
	case fieldAsync:
		return s.async.Selected().Title
	}
	return ""
}

func (s *formScreen) isTextField(f formField) bool {
	switch f {
	case fieldPath, fieldAuthSecret, fieldAuthLookup, fieldActionTarget, fieldActionMeta, fieldTimeout:
		return true
	}
	return false
}

func (s *formScreen) renderField(label string, focused bool, body string) string {
	panel := s.theme.PanelStyle
	if focused {
		panel = s.theme.FocusedPanelStyle
	}
	return panel.Render(s.theme.ValueStyle.Render(label) + "\n\n" + body)
}

func (s *formScreen) authSection() string {
	switch fairwayctl.AuthType(s.authType.Selected().Key) {
	case fairwayctl.AuthBearer:
		s.authSecret.SetHint("Shared bearer secret expected in Authorization.")
		return s.renderField("Bearer secret", s.focus == fieldAuthSecret, s.authSecret.View())
	case fairwayctl.AuthToken:
		s.authSecret.SetHint("Expected token value.")
		secret := s.renderField("Token value", s.focus == fieldAuthSecret, s.authSecret.View())
		s.authLookup.SetHint("Use header name or query:parameter.")
		lookup := s.renderField("Header or query", s.focus == fieldAuthLookup, s.authLookup.View())
		return strings.Join([]string{secret, lookup}, "\n\n")
	default:
		return s.theme.PanelStyle.Render(s.theme.RenderHint("Local-only routes do not require auth fields."))
	}
}

func (s *formScreen) actionSection() string {
	switch fairwayctl.ActionType(s.actionType.Selected().Key) {
	case fairwayctl.ActionCronRun, fairwayctl.ActionCrewRun, fairwayctl.ActionCronEnable, fairwayctl.ActionCronDisable,
		fairwayctl.ActionServiceStart, fairwayctl.ActionServiceStop, fairwayctl.ActionServiceRestart:
		s.actionTgt.SetHint("Shipyard object ID or target name.")
		return s.renderField("Action target", s.focus == fieldActionTarget, s.actionTgt.View())
	case fairwayctl.ActionMessageSend:
		s.actionTgt.SetHint("Optional logical target for the message.")
		target := s.renderField("Message target", s.focus == fieldActionTarget, s.actionTgt.View())
		s.actionMeta.SetHint("Optional provider override.")
		meta := s.renderField("Provider", s.focus == fieldActionMeta, s.actionMeta.View())
		return strings.Join([]string{target, meta}, "\n\n")
	case fairwayctl.ActionTelegramHandle:
		s.actionMeta.SetHint("Optional provider override.")
		return s.renderField("Provider", s.focus == fieldActionMeta, s.actionMeta.View())
	case fairwayctl.ActionHTTPForward:
		s.actionTgt.SetHint("Destination URL starting with http:// or https://.")
		target := s.renderField("Forward URL", s.focus == fieldActionTarget, s.actionTgt.View())
		s.actionMeta.SetHint("Optional HTTP method override, for example POST.")
		meta := s.renderField("Method", s.focus == fieldActionMeta, s.actionMeta.View())
		return strings.Join([]string{target, meta}, "\n\n")
	default:
		return ""
	}
}

func (s *formScreen) submitPanel() string {
	fields := s.visibleFields()
	review := fields[:len(fields)-1]
	lines := make([]string, 0, len(review)+1)
	lines = append(lines, s.theme.ValueStyle.Render("Review"))
	for _, f := range review {
		value := s.fieldValue(f)
		if value == "" {
			value = s.theme.RenderHint("(empty)")
		} else {
			value = s.theme.ValueStyle.Render(value)
		}
		lines = append(lines, s.theme.LabelStyle.Render(s.stepLabel(f)+":")+" "+value)
	}
	label := "Press Enter to create route"
	if s.mode == modeEdit {
		label = "Press Enter to save route"
	}
	lines = append(lines, "", s.theme.RenderHint(label))
	body := strings.Join(lines, "\n")
	if s.focus == fieldSubmit {
		return s.theme.FocusedPanelStyle.Render(body)
	}
	return s.theme.PanelStyle.Render(body)
}

func (s *formScreen) syncFocus() {
	for _, input := range []*components.Input{&s.path, &s.authSecret, &s.authLookup, &s.actionTgt, &s.actionMeta, &s.timeout} {
		input.Blur()
	}
	switch s.focus {
	case fieldPath:
		s.path.Focus()
	case fieldAuthSecret:
		s.authSecret.Focus()
	case fieldAuthLookup:
		s.authLookup.Focus()
	case fieldActionTarget:
		s.actionTgt.Focus()
	case fieldActionMeta:
		s.actionMeta.Focus()
	case fieldTimeout:
		s.timeout.Focus()
	}
}

func (s *formScreen) nextField() formField {
	order := s.visibleFields()
	for i, field := range order {
		if field == s.focus {
			if i+1 >= len(order) {
				return order[len(order)-1]
			}
			return order[i+1]
		}
	}
	return fieldPath
}

func (s *formScreen) prevField() formField {
	order := s.visibleFields()
	for i, field := range order {
		if field == s.focus {
			if i == 0 {
				return order[0]
			}
			return order[i-1]
		}
	}
	return fieldPath
}

func (s *formScreen) visibleFields() []formField {
	fields := []formField{fieldPath, fieldAuthType}
	switch fairwayctl.AuthType(s.authType.Selected().Key) {
	case fairwayctl.AuthBearer:
		fields = append(fields, fieldAuthSecret)
	case fairwayctl.AuthToken:
		fields = append(fields, fieldAuthSecret, fieldAuthLookup)
	}
	fields = append(fields, fieldActionType)
	switch fairwayctl.ActionType(s.actionType.Selected().Key) {
	case fairwayctl.ActionCronRun, fairwayctl.ActionCrewRun, fairwayctl.ActionCronEnable, fairwayctl.ActionCronDisable,
		fairwayctl.ActionServiceStart, fairwayctl.ActionServiceStop, fairwayctl.ActionServiceRestart:
		fields = append(fields, fieldActionTarget)
	case fairwayctl.ActionMessageSend:
		fields = append(fields, fieldActionTarget, fieldActionMeta)
	case fairwayctl.ActionTelegramHandle:
		fields = append(fields, fieldActionMeta)
	case fairwayctl.ActionHTTPForward:
		fields = append(fields, fieldActionTarget, fieldActionMeta)
	}
	fields = append(fields, fieldTimeout, fieldAsync, fieldSubmit)
	return fields
}

func (s *formScreen) snapshot() formState {
	return formState{
		Path:         strings.TrimSpace(s.path.Value()),
		AuthType:     s.authType.Selected().Key,
		AuthSecret:   strings.TrimSpace(s.authSecret.Value()),
		AuthLookup:   strings.TrimSpace(s.authLookup.Value()),
		ActionType:   s.actionType.Selected().Key,
		ActionTarget: strings.TrimSpace(s.actionTgt.Value()),
		ActionMeta:   strings.TrimSpace(s.actionMeta.Value()),
		Timeout:      strings.TrimSpace(s.timeout.Value()),
		Async:        s.async.Selected().Key == "async",
	}
}

// Indireções para testes — substituídas em fairwaywiz_test.go.
var (
	listCrewAgents = func() ([]crewctl.AgentInfo, error) {
		return crewctl.ListAgents("")
	}
	detectCrewAddon = func() addon.Info {
		info, _ := addon.NewRegistry("").Detect(addon.KindCrew)
		return info
	}
)

// buildActionOptions clona actionOptions (declarado em shared.go) e desabilita
// crew.run quando o crew addon não está instalado. allowCrewRun=true preserva
// a opção habilitada no caso de edição de rota legada (rota existente já tinha
// crew.run salvo); sem essa exceção, SetSelectedByKey cairia em fallback e
// trocaria silenciosamente a seleção, perdendo dado do usuário.
func buildActionOptions(crewInstalled, allowCrewRun bool) []components.MenuItem {
	items := make([]components.MenuItem, 0, len(actionOptions))
	for _, item := range actionOptions {
		if item.Key == string(fairwayctl.ActionCrewRun) && !crewInstalled && !allowCrewRun {
			disabled := item
			disabled.Disabled = true
			disabled.Badge = "install crew"
			disabled.BadgeVariant = "muted"
			items = append(items, disabled)
			continue
		}
		items = append(items, item)
	}
	return items
}
