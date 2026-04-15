package cronwiz

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type Root struct {
	theme    theme.Theme
	header   components.Header
	footer   components.Footer
	screen   Screen
	service  CronService
	width    int
	height   int
	quitting bool
	summary  string
}

func NewRoot(service CronService) *Root {
	th := theme.New()
	screen := newMenuScreen(th, service)
	root := &Root{
		theme:   th,
		service: service,
		screen:  screen,
	}
	root.syncChrome()
	return root
}

func (r *Root) Init() tea.Cmd {
	return r.screen.Init()
}

func (r *Root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		r.width = msg.Width
		r.height = msg.Height
		contentWidth := r.theme.ContentWidth(r.width)
		r.header = r.header.SetWidth(contentWidth)
		r.footer = r.footer.SetWidth(contentWidth)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			r.quitting = true
			return r, tea.Quit
		}
	}

	next, cmd := r.screen.Update(msg)
	if next != nil {
		r.screen = next
		r.summary = screenSummary(next)
		r.syncChrome()
	}
	return r, cmd
}

func (r *Root) View() string {
	if r.quitting {
		return ""
	}
	width := r.theme.ContentWidth(r.width)
	body := r.screen.View()
	content := strings.Join([]string{
		r.header.View(),
		"",
		body,
		"",
		r.footer.View(),
	}, "\n")
	return lipgloss.NewStyle().
		Padding(1, theme.PageGutter).
		Width(width + theme.PageGutter*2).
		Render(content)
}

func (r *Root) Summary() string {
	return r.summary
}

func (r *Root) syncChrome() {
	contentWidth := r.theme.ContentWidth(r.width)
	r.header = components.NewHeader(r.theme, r.screen.Title(), r.screen.Breadcrumb()).SetWidth(contentWidth)
	_, isMenu := r.screen.(*menuScreen)
	r.footer = components.NewFooter(r.theme, r.screen.Footer(), isMenu).SetWidth(contentWidth)
}

func (r *Root) setSummary(summary string) {
	r.summary = summary
}

func screenSummary(screen Screen) string {
	switch s := screen.(type) {
	case *addScreen:
		if s.created != nil {
			return "Created cron job " + s.created.ID
		}
	case *updateScreen:
		if s.updated != nil {
			return "Updated cron job " + s.updated.ID
		}
	case *deleteScreen:
		if s.done && s.target != nil && s.err == "" {
			return "Deleted cron job " + s.target.ID
		}
	case *runScreen:
		if s.done && s.target != nil {
			return "Ran cron job " + s.target.ID
		}
	}
	return ""
}
