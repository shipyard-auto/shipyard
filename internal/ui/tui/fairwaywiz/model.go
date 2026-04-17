package fairwaywiz

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
	client   FairwayClient
	width    int
	height   int
	quitting bool
	summary  string
	state    state
}

func NewRoot(client FairwayClient) *Root {
	th := theme.New()
	screen := newMenuScreen(th, client)
	root := &Root{theme: th, client: client, screen: screen, state: stateMenu}
	root.syncChrome()
	return root
}

func (r *Root) Init() tea.Cmd { return r.screen.Init() }

func (r *Root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		r.width = msg.Width
		r.height = msg.Height
		r.syncChrome()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			r.quitting = true
			return r, tea.Quit
		}
	}
	next, cmd := r.screen.Update(msg)
	if next != nil {
		changed := next != r.screen
		r.screen = next
		r.state = next.State()
		r.summary = summaryForScreen(next)
		r.syncChrome()
		if changed && (r.width > 0 || r.height > 0) {
			w, h := r.width, r.height
			sizeCmd := func() tea.Msg { return tea.WindowSizeMsg{Width: w, Height: h} }
			cmd = tea.Batch(cmd, sizeCmd)
		}
	}
	return r, cmd
}

func (r *Root) View() string {
	if r.quitting {
		return ""
	}
	width := r.theme.ContentWidth(r.width)
	content := strings.Join([]string{r.header.View(), "", r.screen.View(), "", r.footer.View()}, "\n")
	return lipgloss.NewStyle().Padding(1, theme.PageGutter).Width(width + theme.PageGutter*2).Render(content)
}

func (r *Root) Summary() string { return r.summary }

func (r *Root) syncChrome() {
	contentWidth := r.theme.ContentWidth(r.width)
	r.header = components.NewHeader(r.theme, r.screen.Title(), r.screen.Breadcrumb()).SetWidth(contentWidth)
	_, isMenu := r.screen.(*menuScreen)
	r.footer = components.NewFooter(r.theme, r.screen.Footer(), isMenu).SetWidth(contentWidth)
}

func summaryForScreen(screen Screen) string {
	switch s := screen.(type) {
	case *formScreen:
		if s.done != nil {
			if s.mode == modeEdit {
				return "Updated route " + s.done.Path
			}
			return "Created route " + s.done.Path
		}
	case *deleteScreen:
		if s.donePath != "" {
			return "Deleted route " + s.donePath
		}
	case *statusScreen:
		if s.loaded {
			return "Loaded fairway status"
		}
	}
	return ""
}
