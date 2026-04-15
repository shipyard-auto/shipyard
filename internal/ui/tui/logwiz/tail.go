package logwiz

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type tailTickMsg time.Time

type tailScreen struct {
	theme     theme.Theme
	service   LogsService
	source    components.Input
	entityID  components.Input
	step      int
	viewer    components.Viewer
	lastTick  time.Time
	hasEvents bool
}

func newTailScreen(th theme.Theme, service LogsService) Screen {
	source := components.NewInput(th, "Source", "cron", nil)
	source.SetValue(logs.DefaultSourceCron)
	entity := components.NewInput(th, "Entity ID", "AB12CD", nil)
	return &tailScreen{theme: th, service: service, source: source, entityID: entity, viewer: components.NewViewer(th, "")}
}

func (s *tailScreen) Init() tea.Cmd { return s.source.Init() }
func (s *tailScreen) Title() string { return "Tail Live Events" }
func (s *tailScreen) Breadcrumb() []string { return []string{"logs", "tail"} }
func (s *tailScreen) Footer() []components.KeyHint {
	if s.step == 0 {
		return []components.KeyHint{{Key: "enter", Label: "start"}}
	}
	return []components.KeyHint{{Key: "esc", Label: "stop tailing"}}
}
func (s *tailScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "esc" {
			return newMenuScreen(s.theme, s.service), nil
		}
	case tailTickMsg:
		events, _ := s.service.Query(logs.Query{Source: strings.TrimSpace(s.source.Value()), Entity: strings.ToUpper(strings.TrimSpace(s.entityID.Value())), Limit: 100})
		lines := []string{}
		for _, event := range events {
			lines = append(lines, renderEvents([]logs.Event{event}, ""))
		}
		if len(lines) > 0 {
			s.hasEvents = true
		}
		s.viewer.SetContent(strings.Join(lines, "\n"))
		return s, tea.Tick(time.Second, func(t time.Time) tea.Msg { return tailTickMsg(t) })
	}
	if s.step == 0 {
		cmd, submitted := s.source.Update(msg)
		_ = cmd
		if submitted {
			s.step = 1
			return s, s.entityID.Init()
		}
		if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
			s.step = 1
			return s, s.entityID.Init()
		}
		return s, nil
	}
	if s.step == 1 {
		_, submitted := s.entityID.Update(msg)
		if submitted {
			s.step = 2
			return s, tea.Tick(time.Second, func(t time.Time) tea.Msg { return tailTickMsg(t) })
		}
		return s, nil
	}
	viewer, cmd := s.viewer.Update(msg)
	s.viewer = viewer
	return s, cmd
}
func (s *tailScreen) View() string {
	switch s.step {
	case 0:
		return s.source.View()
	case 1:
		return s.entityID.View()
	default:
		if !s.hasEvents {
			empty := components.NewEmpty(s.theme, components.EmptyProps{Icon: "⋯", Title: "Waiting for events…", Hint: "[esc] stop tailing"})
			return empty.View()
		}
		return s.theme.RenderSuccess("● live") + "\n\n" + s.viewer.View()
	}
}
