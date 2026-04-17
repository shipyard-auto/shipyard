package servicewiz

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	svcpkg "github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type statusMsg struct {
	record svcpkg.ServiceRecord
	status svcpkg.RuntimeStatus
	err    error
}

type statusScreen struct {
	theme    theme.Theme
	service  ServiceAPI
	selectID string
	picker   components.Menu
	spinner  components.Spinner
	record   *svcpkg.ServiceRecord
	status   svcpkg.RuntimeStatus
	err      string
	loading  bool
}

func newStatusScreen(th theme.Theme, service ServiceAPI, preselectedID string) Screen {
	s := &statusScreen{theme: th, service: service, selectID: preselectedID, spinner: components.NewSpinner(th, "Loading service status…")}
	s.bootstrap()
	return s
}

func (s *statusScreen) bootstrap() {
	if s.selectID != "" {
		records, status, err := s.service.Status(s.selectID)
		if err == nil {
			s.record = &records
			s.status = status
			return
		}
	}
	records, _ := s.service.List()
	s.picker = components.NewMenu(s.theme, recordsToMenuItems(records))
}

func (s *statusScreen) Init() tea.Cmd        { return nil }
func (s *statusScreen) Title() string        { return "Service Status" }
func (s *statusScreen) Breadcrumb() []string { return []string{"service", "status"} }
func (s *statusScreen) Footer() []components.KeyHint {
	if s.record == nil {
		return []components.KeyHint{{Key: "↑↓", Label: "choose"}, {Key: "enter", Label: "select"}, {Key: "esc", Label: "back"}}
	}
	return []components.KeyHint{{Key: "esc", Label: "back"}}
}

func (s *statusScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case statusMsg:
		s.loading = false
		if msg.err != nil {
			s.err = msg.err.Error()
			return s, nil
		}
		s.record = &msg.record
		s.status = msg.status
		return s, nil
	case tea.KeyMsg:
		if msg.String() == "esc" {
			return newMenuScreen(s.theme, s.service), nil
		}
	}
	if s.record == nil {
		records, _ := s.service.List()
		if len(records) == 0 {
			empty := components.NewEmpty(s.theme, components.EmptyProps{Icon: "⎋", Title: "No services to inspect.", Hint: "[esc] back"})
			return &staticScreen{theme: s.theme, title: s.Title(), crumb: s.Breadcrumb(), footer: s.Footer(), body: empty.View(), next: newMenuScreen(s.theme, s.service)}, nil
		}
		menu, cmd := s.picker.Update(msg)
		s.picker = menu
		if cmd != nil {
			id := cmd().(components.MenuSelectedMsg).Key
			s.loading = true
			return s, tea.Batch(s.spinner.Init(), func() tea.Msg {
				record, status, err := s.service.Status(id)
				return statusMsg{record: record, status: status, err: err}
			})
		}
		return s, nil
	}
	if s.loading {
		spinner, cmd := s.spinner.Update(msg)
		s.spinner = spinner
		return s, cmd
	}
	return s, nil
}

func (s *statusScreen) View() string {
	if s.err != "" {
		return s.theme.RenderError(s.err)
	}
	if s.record == nil {
		return s.picker.View()
	}
	return renderReview(s.theme, "Runtime status", [][2]string{
		{"ID", s.record.ID},
		{"Name", s.record.Name},
		{"State", fallback(s.status.State, "unknown")},
		{"SubState", blankOrValue(s.status.SubState)},
		{"PID", fmt.Sprintf("%d", s.status.PID)},
		{"EnabledAt", blankOrValue(s.status.EnabledAt)},
		{"Since", blankOrValue(s.status.SinceHint)},
		{"Last Exit", fmt.Sprintf("%d", s.status.LastExit)},
	}, nil)
}
