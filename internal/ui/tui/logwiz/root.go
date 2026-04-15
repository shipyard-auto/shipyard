package logwiz

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type LogsService interface {
	LoadConfig() (logs.Config, error)
	SetRetentionDays(days int) (logs.Config, error)
	ListSources() ([]logs.SourceSummary, error)
	Query(query logs.Query) ([]logs.Event, error)
	Prune() (logs.PruneResult, error)
}

type Screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Screen, tea.Cmd)
	View() string
	Title() string
	Breadcrumb() []string
	Footer() []components.KeyHint
}

type Root struct {
	theme   theme.Theme
	service LogsService
	screen  Screen
	header  components.Header
	footer  components.Footer
	width   int
	height  int
}

func NewRoot(service LogsService) *Root {
	th := theme.New()
	screen := newMenuScreen(th, service)
	r := &Root{theme: th, service: service, screen: screen}
	r.syncChrome()
	return r
}

func (r *Root) Init() tea.Cmd { return r.screen.Init() }

func (r *Root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "q", "ctrl+c":
			return r, tea.Quit
		}
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		r.width, r.height = size.Width, size.Height
	}
	next, cmd := r.screen.Update(msg)
	if next != nil {
		r.screen = next
		r.syncChrome()
	}
	return r, cmd
}

func (r *Root) View() string {
	body := strings.Join([]string{
		r.header.View(),
		"",
		r.screen.View(),
		"",
		r.footer.View(),
	}, "\n")
	return lipgloss.NewStyle().Padding(1, 2).Width(r.theme.ContentWidth(r.width)).Render(body)
}

func (r *Root) syncChrome() {
	r.header = components.NewHeader(r.theme, r.screen.Title(), r.screen.Breadcrumb())
	r.footer = components.NewFooter(r.theme, r.screen.Footer(), len(r.screen.Breadcrumb()) == 0)
}
