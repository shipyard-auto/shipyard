package logwiz

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type retentionScreen struct {
	theme   theme.Theme
	service LogsService
	input   components.Input
	done    bool
	value   int
}

func newRetentionScreen(th theme.Theme, service LogsService) Screen {
	cfg, _ := service.LoadConfig()
	in := components.NewInput(th, "Retention (days)", "14", func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 1 || n > 3650 {
			return fmt.Errorf("retention must be between 1 and 3650 days")
		}
		return nil
	})
	in.SetValue(fmt.Sprintf("%d", cfg.RetentionDays))
	return &retentionScreen{theme: th, service: service, input: in, value: cfg.RetentionDays}
}

func (s *retentionScreen) Init() tea.Cmd        { return s.input.Init() }
func (s *retentionScreen) Title() string        { return "Configure Retention" }
func (s *retentionScreen) Breadcrumb() []string { return []string{"logs", "retention"} }
func (s *retentionScreen) Footer() []components.KeyHint {
	return []components.KeyHint{{Key: "enter", Label: "save"}}
}
func (s *retentionScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == "esc" {
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.done && key.String() == "enter" {
			return newMenuScreen(s.theme, s.service), nil
		}
	}
	_, submitted := s.input.Update(msg)
	if submitted {
		n, _ := strconv.Atoi(strings.TrimSpace(s.input.Value()))
		cfg, err := s.service.SetRetentionDays(n)
		if err == nil {
			s.value = cfg.RetentionDays
			s.done = true
		}
	}
	return s, nil
}
func (s *retentionScreen) View() string {
	if s.done {
		return s.theme.RenderSuccess(fmt.Sprintf("Retention updated to %d days.", s.value))
	}
	return s.input.View() + "\n\n" + s.theme.RenderHint(fmt.Sprintf("Logs older than %s days will be deleted on next prune.", strings.TrimSpace(s.input.Value())))
}
