package servicewiz

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	svcpkg "github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type deleteScreen struct {
	theme    theme.Theme
	service  ServiceAPI
	selectID string
	picker   components.Menu
	target   *svcpkg.ServiceRecord
	confirm  *components.Confirm
	done     bool
	err      string
}

func newDeleteScreen(th theme.Theme, service ServiceAPI, preselectedID string) Screen {
	s := &deleteScreen{theme: th, service: service, selectID: preselectedID}
	s.bootstrap()
	return s
}

func (s *deleteScreen) bootstrap() {
	if s.selectID != "" {
		if record, err := s.service.Get(s.selectID); err == nil {
			s.target = &record
			confirm := components.NewConfirm(s.theme, fmt.Sprintf("Delete %s — %s? This cannot be undone.", record.ID, record.Name), true)
			s.confirm = &confirm
			return
		}
	}
	records, _ := s.service.List()
	s.picker = components.NewMenu(s.theme, recordsToMenuItems(records))
}

func (s *deleteScreen) Init() tea.Cmd        { return nil }
func (s *deleteScreen) Title() string        { return "Delete Service" }
func (s *deleteScreen) Breadcrumb() []string { return []string{"service", "delete"} }
func (s *deleteScreen) Footer() []components.KeyHint {
	if s.done {
		return []components.KeyHint{{Key: "enter", Label: "menu"}}
	}
	if s.target == nil {
		return []components.KeyHint{{Key: "↑↓", Label: "choose"}, {Key: "enter", Label: "select"}, {Key: "esc", Label: "back"}}
	}
	return []components.KeyHint{{Key: "←→", Label: "choose"}, {Key: "enter", Label: "select"}, {Key: "esc", Label: "back"}}
}

func (s *deleteScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == "esc" {
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.done && key.String() == "enter" {
			return newMenuScreen(s.theme, s.service), nil
		}
	}
	if s.target == nil {
		records, _ := s.service.List()
		if len(records) == 0 {
			empty := components.NewEmpty(s.theme, components.EmptyProps{Icon: "⎋", Title: "No services to delete.", Hint: "[esc] back"})
			return &staticScreen{theme: s.theme, title: s.Title(), crumb: s.Breadcrumb(), footer: s.Footer(), body: empty.View(), next: newMenuScreen(s.theme, s.service)}, nil
		}
		menu, cmd := s.picker.Update(msg)
		s.picker = menu
		if cmd != nil {
			record, err := s.service.Get(cmd().(components.MenuSelectedMsg).Key)
			if err != nil {
				s.err = err.Error()
				return s, nil
			}
			s.target = &record
			confirm := components.NewConfirm(s.theme, fmt.Sprintf("Delete %s — %s? This cannot be undone.", record.ID, record.Name), true)
			s.confirm = &confirm
		}
		return s, nil
	}
	confirm, cmd := s.confirm.Update(msg)
	s.confirm = &confirm
	if cmd != nil {
		decision := cmd().(components.ConfirmMsg)
		if !decision.Accepted {
			return newMenuScreen(s.theme, s.service), nil
		}
		if err := s.service.Delete(s.target.ID); err != nil {
			s.err = err.Error()
		} else {
			s.done = true
		}
	}
	return s, nil
}

func (s *deleteScreen) View() string {
	if s.done {
		return s.theme.RenderSuccess("Service deleted successfully.") + "\n\n" +
			renderReview(s.theme, "Deleted service", [][2]string{{"ID", s.target.ID}, {"Name", s.target.Name}}, nil)
	}
	if s.err != "" {
		return s.theme.RenderError(s.err)
	}
	if s.target == nil {
		return s.picker.View()
	}
	return s.confirm.View()
}

type staticScreen struct {
	theme  theme.Theme
	title  string
	crumb  []string
	footer []components.KeyHint
	body   string
	next   Screen
}

func (s *staticScreen) Init() tea.Cmd                { return nil }
func (s *staticScreen) Title() string                { return s.title }
func (s *staticScreen) Breadcrumb() []string         { return s.crumb }
func (s *staticScreen) Footer() []components.KeyHint { return s.footer }
func (s *staticScreen) View() string                 { return s.body }
func (s *staticScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		return s.next, nil
	}
	return s, nil
}
