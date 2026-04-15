package logwiz

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type pruneScreen struct {
	theme   theme.Theme
	service LogsService
	confirm components.Confirm
	done    bool
	body    string
}

func newPruneScreen(th theme.Theme, service LogsService) Screen {
	cfg, _ := service.LoadConfig()
	return &pruneScreen{
		theme:   th,
		service: service,
		confirm: components.NewConfirm(th, fmt.Sprintf("Delete files older than %d days?", cfg.RetentionDays), true),
	}
}

func (s *pruneScreen) Init() tea.Cmd { return nil }
func (s *pruneScreen) Title() string { return "Prune Old Logs" }
func (s *pruneScreen) Breadcrumb() []string { return []string{"logs", "prune"} }
func (s *pruneScreen) Footer() []components.KeyHint { return []components.KeyHint{{Key: "enter", Label: "confirm"}} }
func (s *pruneScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == "esc" {
			return newMenuScreen(s.theme, s.service), nil
		}
		if s.done && key.String() == "enter" {
			return newMenuScreen(s.theme, s.service), nil
		}
	}
	confirm, cmd := s.confirm.Update(msg)
	s.confirm = confirm
	if cmd != nil {
		result := cmd().(components.ConfirmMsg)
		if !result.Accepted {
			return newMenuScreen(s.theme, s.service), nil
		}
		pruned, err := s.service.Prune()
		if err != nil {
			s.body = s.theme.RenderError(err.Error())
			s.done = true
			return s, nil
		}
		if pruned.DeletedFiles == 0 {
			empty := components.NewEmpty(s.theme, components.EmptyProps{
				Icon:        "✓",
				Title:       "Nothing to prune.",
				Description: "All log files are within the retention window.",
				Hint:        "[esc] back",
			})
			s.body = empty.View()
			s.done = true
			return s, nil
		}
		s.body = s.theme.RenderSuccess(fmt.Sprintf("Deleted %d files and freed %d bytes.", pruned.DeletedFiles, pruned.FreedBytes))
		s.done = true
	}
	return s, nil
}
func (s *pruneScreen) View() string {
	if s.done {
		return s.body
	}
	return s.confirm.View()
}
