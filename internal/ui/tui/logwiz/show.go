package logwiz

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type showScreen struct {
	theme      theme.Theme
	service    LogsService
	sourceMenu components.Menu
	entityID   components.Input
	levelMenu  components.Menu
	limit      components.Input
	filter     components.Input
	step       int
	field      int
	viewer     components.Viewer
	events     []logs.Event
	filtering  bool
}

func newShowScreen(th theme.Theme, service LogsService, source string) Screen {
	sourceMenu := components.NewMenu(th, []components.MenuItem{
		{Title: "All sources", Description: "Show events from every source", Key: ""},
		{Title: "cron", Description: "Show cron subsystem events", Key: logs.DefaultSourceCron},
	})
	if source != "" {
		sourceMenu.SetSelectedByKey(source)
	}
	levelMenu := components.NewMenu(th, []components.MenuItem{
		{Title: "All levels", Description: "Include every level", Key: ""},
		{Title: "info", Description: "Informational events", Key: "info"},
		{Title: "warn", Description: "Warnings only", Key: "warn"},
		{Title: "error", Description: "Errors only", Key: "error"},
	})
	id := components.NewInput(th, "Entity ID", "AB12CD", nil)
	limit := components.NewInput(th, "Limit", "50", func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 1 || n > 500 {
			return fmt.Errorf("limit must be between 1 and 500")
		}
		return nil
	})
	limit.SetValue("50")
	filter := components.NewInput(th, "Filter", "substring", nil)
	return &showScreen{
		theme:      th,
		service:    service,
		sourceMenu: sourceMenu,
		entityID:   id,
		levelMenu:  levelMenu,
		limit:      limit,
		filter:     filter,
		viewer:     components.NewViewer(th, ""),
	}
}

func (s *showScreen) Init() tea.Cmd        { return nil }
func (s *showScreen) Title() string        { return "Show Recent Events" }
func (s *showScreen) Breadcrumb() []string { return []string{"logs", "show"} }

func (s *showScreen) Footer() []components.KeyHint {
	if s.step == 0 {
		return []components.KeyHint{{Key: "enter", Label: "next"}, {Key: "esc", Label: "back"}}
	}
	if s.filtering {
		return []components.KeyHint{{Key: "enter", Label: "apply"}, {Key: "esc", Label: "close filter"}}
	}
	return []components.KeyHint{{Key: "↑↓", Label: "scroll"}, {Key: "/", Label: "filter"}, {Key: "esc", Label: "change filters"}}
}

func (s *showScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			if s.filtering {
				s.filtering = false
				return s, nil
			}
			if s.step == 1 {
				s.step = 0
				return s, nil
			}
			return newMenuScreen(s.theme, s.service), nil
		case "/":
			if s.step == 1 {
				s.filtering = true
				return s, s.filter.Init()
			}
		}
	}

	if s.step == 0 {
		switch s.field {
		case 0:
			menu, cmd := s.sourceMenu.Update(msg)
			s.sourceMenu = menu
			if cmd != nil {
				s.field = 1
				return s, s.entityID.Init()
			}
		case 1:
			cmd, submitted := s.entityID.Update(msg)
			if submitted {
				s.field = 2
				return s, nil
			}
			return s, cmd
		case 2:
			menu, cmd := s.levelMenu.Update(msg)
			s.levelMenu = menu
			if cmd != nil {
				s.field = 3
				return s, s.limit.Init()
			}
		case 3:
			cmd, submitted := s.limit.Update(msg)
			if submitted {
				s.events, _ = s.service.Query(logs.Query{
					Source: strings.TrimSpace(s.sourceMenu.Selected().Key),
					Entity: strings.ToUpper(strings.TrimSpace(s.entityID.Value())),
					Level:  strings.TrimSpace(s.levelMenu.Selected().Key),
					Limit:  parseLimitOrDefault(s.limit.Value(), 50),
				})
				s.viewer.SetContent(renderEvents(s.events, ""))
				s.step = 1
				return s, nil
			}
			return s, cmd
		}
		return s, nil
	}

	if s.filtering {
		_, submitted := s.filter.Update(msg)
		s.viewer.SetContent(renderEvents(s.events, s.filter.Value()))
		if submitted {
			s.filtering = false
		}
		return s, nil
	}

	viewer, cmd := s.viewer.Update(msg)
	s.viewer = viewer
	return s, cmd
}

func (s *showScreen) View() string {
	if s.step == 0 {
		sections := []string{
			s.theme.LabelStyle.Render("Source"),
			s.sourceMenu.View(),
			s.entityID.View(),
			s.theme.LabelStyle.Render("Level"),
			s.levelMenu.View(),
			s.limit.View(),
		}
		return strings.Join(sections, "\n\n")
	}

	content := s.viewer.View()
	if strings.TrimSpace(content) == "" {
		empty := components.NewEmpty(s.theme, components.EmptyProps{Icon: "⎋", Title: "No log events match these filters.", Hint: "[esc] change filters"})
		return empty.View()
	}
	if s.filtering {
		return s.filter.View() + "\n\n" + content
	}
	return content
}

func parseLimitOrDefault(v string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func renderEvents(events []logs.Event, substr string) string {
	lines := []string{}
	substr = strings.ToLower(strings.TrimSpace(substr))
	for _, event := range events {
		line := fmt.Sprintf("%s  [%s]  %s/%s  %s",
			event.Timestamp.Format("15:04:05"),
			strings.ToUpper(event.Level),
			event.Source,
			event.EntityID,
			event.Message,
		)
		if substr != "" && !strings.Contains(strings.ToLower(line), substr) {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func eventAfter(event logs.Event, since time.Time) bool {
	return event.Timestamp.After(since)
}
