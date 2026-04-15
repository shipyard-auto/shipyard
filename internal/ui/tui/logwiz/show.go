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
	theme     theme.Theme
	service   LogsService
	source    components.Input
	entityID  components.Input
	level     components.Input
	limit     components.Input
	filter    components.Input
	step      int
	field     int
	viewer    components.Viewer
	events    []logs.Event
	filtering bool
	prefill   string
}

func newShowScreen(th theme.Theme, service LogsService, source string) Screen {
	src := components.NewInput(th, "Source", "cron or empty", nil)
	src.SetValue(source)
	id := components.NewInput(th, "Entity ID", "AB12CD", nil)
	level := components.NewInput(th, "Level", "info|warn|error", nil)
	limit := components.NewInput(th, "Limit", "50", func(v string) error {
		if strings.TrimSpace(v) == "" {
			return nil
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 1 || n > 500 {
			return fmt.Errorf("limit must be between 1 and 500")
		}
		return nil
	})
	limit.SetValue("50")
	filter := components.NewInput(th, "Filter", "substring", nil)
	return &showScreen{theme: th, service: service, source: src, entityID: id, level: level, limit: limit, filter: filter, viewer: components.NewViewer(th, ""), prefill: source}
}

func (s *showScreen) Init() tea.Cmd { return s.source.Init() }
func (s *showScreen) Title() string { return "Show Recent Events" }
func (s *showScreen) Breadcrumb() []string { return []string{"logs", "show"} }
func (s *showScreen) Footer() []components.KeyHint {
	if s.step == 0 {
		return []components.KeyHint{{Key: "enter", Label: "next"}}
	}
	return []components.KeyHint{{Key: "↑↓", Label: "scroll"}, {Key: "/", Label: "filter"}}
}

func (s *showScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == "esc" {
			if s.step == 1 {
				s.step = 0
				return s, nil
			}
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.step == 1 && key.String() == "/" {
			s.filtering = true
			return s, s.filter.Init()
		}
	}
	if s.step == 0 {
		inputs := []*components.Input{&s.source, &s.entityID, &s.level, &s.limit}
		cmd, submitted := inputs[s.field].Update(msg)
		if submitted {
			if s.field == len(inputs)-1 {
				limit := 50
				if strings.TrimSpace(s.limit.Value()) != "" {
					limit, _ = strconv.Atoi(strings.TrimSpace(s.limit.Value()))
				}
				events, _ := s.service.Query(logs.Query{
					Source: strings.TrimSpace(s.source.Value()),
					Entity: strings.ToUpper(strings.TrimSpace(s.entityID.Value())),
					Level:  strings.TrimSpace(s.level.Value()),
					Limit:  limit,
				})
				s.events = events
				s.viewer.SetContent(renderEvents(events, ""))
				s.step = 1
				return s, nil
			}
			s.field++
			inputs[s.field].Focus()
			return s, inputs[s.field].Init()
		}
		return s, cmd
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
		return strings.Join([]string{s.source.View(), "", s.entityID.View(), "", s.level.View(), "", s.limit.View()}, "\n")
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
