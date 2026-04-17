package logwiz

import (
	"io"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type tailTickMsg time.Time

type tailScreen struct {
	theme      theme.Theme
	service    LogsService
	sourceMenu components.Menu
	entityID   components.Input
	levelMenu  components.Menu
	step       int
	field      int
	viewer     components.Viewer
	hasEvents  bool
	stop       chan struct{}
	lines      []string
	writer     *tailWriter
}

func newTailScreen(th theme.Theme, service LogsService) Screen {
	sourceMenu := components.NewMenu(th, []components.MenuItem{
		{Title: "All sources", Description: "Tail every source", Key: ""},
		{Title: "cron", Description: "Tail cron subsystem events", Key: logs.DefaultSourceCron},
	})
	sourceMenu.SetSelectedByKey(logs.DefaultSourceCron)
	levelMenu := components.NewMenu(th, []components.MenuItem{
		{Title: "All levels", Description: "Tail every level", Key: ""},
		{Title: "info", Description: "Informational events", Key: "info"},
		{Title: "warn", Description: "Warnings only", Key: "warn"},
		{Title: "error", Description: "Errors only", Key: "error"},
	})
	entity := components.NewInput(th, "Entity ID", "AB12CD", nil)
	return &tailScreen{
		theme:      th,
		service:    service,
		sourceMenu: sourceMenu,
		entityID:   entity,
		levelMenu:  levelMenu,
		viewer:     components.NewViewer(th, ""),
	}
}

func (s *tailScreen) Init() tea.Cmd        { return nil }
func (s *tailScreen) Title() string        { return "Tail Live Events" }
func (s *tailScreen) Breadcrumb() []string { return []string{"logs", "tail"} }

func (s *tailScreen) Footer() []components.KeyHint {
	if s.step < 3 {
		return []components.KeyHint{{Key: "enter", Label: "next"}, {Key: "esc", Label: "back"}}
	}
	return []components.KeyHint{{Key: "esc", Label: "stop tailing"}}
}

func (s *tailScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "esc" {
			if s.stop != nil {
				close(s.stop)
				s.stop = nil
			}
			return newMenuScreen(s.theme, s.service), nil
		}
	case tailTickMsg:
		if s.writer != nil {
			for _, chunk := range s.writer.Drain() {
				chunk = strings.TrimSpace(chunk)
				if chunk == "" {
					continue
				}
				s.hasEvents = true
				s.lines = append(s.lines, strings.Split(chunk, "\n")...)
			}
			s.viewer.SetContent(strings.Join(s.lines, "\n"))
		}
		return s, tea.Tick(time.Second, func(t time.Time) tea.Msg { return tailTickMsg(t) })
	}

	switch s.step {
	case 0:
		menu, cmd := s.sourceMenu.Update(msg)
		s.sourceMenu = menu
		if cmd != nil {
			s.step = 1
			return s, s.entityID.Init()
		}
		return s, nil
	case 1:
		cmd, submitted := s.entityID.Update(msg)
		if submitted {
			s.step = 2
			return s, nil
		}
		return s, cmd
	case 2:
		menu, cmd := s.levelMenu.Update(msg)
		s.levelMenu = menu
		if cmd != nil {
			s.step = 3
			s.stop = make(chan struct{})
			s.writer = &tailWriter{}
			query := logs.Query{
				Source: strings.TrimSpace(s.sourceMenu.Selected().Key),
				Entity: strings.ToUpper(strings.TrimSpace(s.entityID.Value())),
				Level:  strings.TrimSpace(s.levelMenu.Selected().Key),
			}
			return s, tea.Batch(
				tea.Tick(time.Second, func(t time.Time) tea.Msg { return tailTickMsg(t) }),
				startTailCmd(s.service, query, s.writer, s.stop),
			)
		}
		return s, nil
	default:
		viewer, cmd := s.viewer.Update(msg)
		s.viewer = viewer
		return s, cmd
	}
}

func (s *tailScreen) View() string {
	switch s.step {
	case 0:
		return strings.Join([]string{s.theme.LabelStyle.Render("Source"), s.sourceMenu.View()}, "\n\n")
	case 1:
		return s.entityID.View()
	case 2:
		return strings.Join([]string{s.theme.LabelStyle.Render("Level"), s.levelMenu.View()}, "\n\n")
	default:
		if !s.hasEvents {
			empty := components.NewEmpty(s.theme, components.EmptyProps{Icon: "⋯", Title: "Waiting for events…", Hint: "[esc] stop tailing"})
			return empty.View()
		}
		return s.theme.RenderSuccess("● live") + "\n\n" + s.viewer.View()
	}
}

func startTailCmd(service LogsService, query logs.Query, writer io.Writer, stop <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		go func() {
			_ = service.Tail(query, writer, stop)
		}()
		return nil
	}
}

type tailWriter struct {
	mu      sync.Mutex
	pending []string
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, string(p))
	return len(p), nil
}

func (w *tailWriter) Drain() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := append([]string{}, w.pending...)
	w.pending = nil
	return out
}

var _ io.Writer = (*tailWriter)(nil)
