package servicewiz

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type batchKind string

const (
	batchStart   batchKind = "start"
	batchStop    batchKind = "stop"
	batchRestart batchKind = "restart"
	batchEnable  batchKind = "enable"
	batchDisable batchKind = "disable"
)

type batchStepMsg struct {
	id  string
	err error
}

type batchScreen struct {
	theme      theme.Theme
	service    ServiceAPI
	title      string
	breadcrumb []string
	kind       batchKind
	checklist  *components.Checklist
	empty      *components.Empty
	result     string
	successes  []string
	failures   []string
	pending    []string
	total      int
	index      int
	running    bool
	spinner    components.Spinner
}

func newStartScreen(th theme.Theme, service ServiceAPI) Screen {
	return newBatchScreen(th, service, batchStart)
}
func newStopScreen(th theme.Theme, service ServiceAPI) Screen {
	return newBatchScreen(th, service, batchStop)
}
func newRestartScreen(th theme.Theme, service ServiceAPI) Screen {
	return newBatchScreen(th, service, batchRestart)
}
func newEnableScreen(th theme.Theme, service ServiceAPI) Screen {
	return newBatchScreen(th, service, batchEnable)
}
func newDisableScreen(th theme.Theme, service ServiceAPI) Screen {
	return newBatchScreen(th, service, batchDisable)
}

func newBatchScreen(th theme.Theme, service ServiceAPI, kind batchKind) Screen {
	s := &batchScreen{
		theme:      th,
		service:    service,
		kind:       kind,
		title:      map[batchKind]string{batchStart: "Start Services", batchStop: "Stop Services", batchRestart: "Restart Services", batchEnable: "Enable Services", batchDisable: "Disable Services"}[kind],
		breadcrumb: []string{"service", string(kind)},
		spinner:    components.NewSpinner(th, ""),
	}
	s.refresh()
	return s
}

func (s *batchScreen) refresh() {
	records, _ := s.service.List()
	items := []components.ChecklistItem{}
	for _, record := range records {
		if s.kind == batchEnable && record.Enabled {
			continue
		}
		if s.kind == batchDisable && !record.Enabled {
			continue
		}
		items = append(items, components.ChecklistItem{ID: record.ID, Title: fmt.Sprintf("%s  %s", record.ID, record.Name), Subtitle: compactCommand(record.Command)})
	}
	if len(items) == 0 {
		props := components.EmptyProps{Hint: "[esc] back"}
		switch s.kind {
		case batchEnable:
			props.Icon, props.Title = "✓", "All services are already enabled."
		case batchDisable:
			props.Icon, props.Title = "○", "No enabled services to disable."
		case batchStart:
			props.Icon, props.Title = "▶", "No services to start."
		case batchStop:
			props.Icon, props.Title = "■", "No services to stop."
		case batchRestart:
			props.Icon, props.Title = "↻", "No services to restart."
		}
		empty := components.NewEmpty(s.theme, props)
		s.empty = &empty
		s.checklist = nil
		return
	}
	cl := components.NewChecklist(s.theme, items)
	s.checklist = &cl
	s.empty = nil
}

func (s *batchScreen) Init() tea.Cmd        { return nil }
func (s *batchScreen) Title() string        { return s.title }
func (s *batchScreen) Breadcrumb() []string { return s.breadcrumb }
func (s *batchScreen) Footer() []components.KeyHint {
	if s.running {
		return []components.KeyHint{{Key: "esc", Label: "cancel"}}
	}
	return []components.KeyHint{{Key: "space", Label: "toggle"}, {Key: "a", Label: "select all"}, {Key: "enter", Label: string(s.kind) + " selected"}, {Key: "esc", Label: "cancel"}}
}

func (s *batchScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case batchStepMsg:
		if msg.id != "" {
			if msg.err != nil {
				s.failures = append(s.failures, fmt.Sprintf("%s: %v", msg.id, msg.err))
			} else {
				s.successes = append(s.successes, msg.id)
			}
			s.index++
		}
		if s.index >= len(s.pending) {
			s.running = false
			s.result = s.renderResult()
			s.refresh()
			return s, nil
		}
		s.spinner = components.NewSpinner(s.theme, s.progressLabel())
		return s, tea.Batch(s.spinner.Init(), s.executePending())
	case tea.KeyMsg:
		if msg.String() == "esc" {
			if s.running {
				s.running = false
				s.pending = nil
				s.result = s.theme.RenderError("Operation cancelled.")
				return s, nil
			}
			return newMenuScreen(s.theme, s.service), nil
		}
	}
	if s.running {
		spinner, cmd := s.spinner.Update(msg)
		s.spinner = spinner
		return s, cmd
	}
	if s.empty != nil {
		return s, nil
	}
	checklist, cmd := s.checklist.Update(msg)
	s.checklist = &checklist
	if cmd != nil {
		selected := cmd().(components.ChecklistConfirmedMsg)
		if len(selected.SelectedIDs) == 0 {
			s.result = s.theme.RenderError("No services selected.")
			return s, nil
		}
		s.pending = append([]string{}, selected.SelectedIDs...)
		s.total = len(s.pending)
		s.index = 0
		s.successes = nil
		s.failures = nil
		s.result = ""
		s.running = true
		s.spinner = components.NewSpinner(s.theme, s.progressLabel())
		return s, tea.Batch(s.spinner.Init(), s.executePending())
	}
	return s, nil
}

func (s *batchScreen) View() string {
	if s.empty != nil {
		return s.empty.View()
	}
	if s.running {
		return s.spinner.View()
	}
	if s.result != "" {
		return s.result
	}
	return s.checklist.View()
}

func (s *batchScreen) progressLabel() string {
	action := strings.Title(string(s.kind))
	if s.total == 0 {
		return action + "…"
	}
	return fmt.Sprintf("%sing %d of %d…", action, s.index+1, s.total)
}

func (s *batchScreen) executePending() tea.Cmd {
	if s.index >= len(s.pending) {
		return func() tea.Msg { return batchStepMsg{} }
	}
	id := s.pending[s.index]
	return func() tea.Msg {
		var err error
		switch s.kind {
		case batchStart:
			_, err = s.service.Start(id)
		case batchStop:
			_, err = s.service.Stop(id)
		case batchRestart:
			_, err = s.service.Restart(id)
		case batchEnable:
			_, err = s.service.Enable(id)
		case batchDisable:
			_, err = s.service.Disable(id)
		}
		return batchStepMsg{id: id, err: err}
	}
}

func (s *batchScreen) renderResult() string {
	lines := []string{}
	if len(s.successes) > 0 {
		lines = append(lines, s.theme.RenderSuccess(strings.Join(s.successes, "\n")))
	}
	if len(s.failures) > 0 {
		lines = append(lines, s.theme.RenderError(strings.Join(s.failures, "\n")))
	}
	return strings.Join(lines, "\n\n")
}

var _ Screen = (*batchScreen)(nil)
