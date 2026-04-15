package components

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type KeyHint struct {
	Key   string
	Label string
}

type Header struct {
	theme      theme.Theme
	title      string
	breadcrumb []string
	width      int
}

func NewHeader(th theme.Theme, title string, breadcrumb []string) Header {
	return Header{theme: th, title: title, breadcrumb: breadcrumb}
}

func (h Header) Init() tea.Cmd { return nil }

func (h Header) Update(msg tea.Msg) (Header, tea.Cmd) {
	if resize, ok := msg.(tea.WindowSizeMsg); ok {
		h.Resize(resize.Width, resize.Height)
	}
	return h, nil
}

func (h *Header) Resize(width, _ int) {
	h.width = width
}

func (h Header) View() string {
	brand := h.theme.TitleStyle.Render("⛵ SHIPYARD")
	title := h.theme.ValueStyle.Render(h.title)
	row := lipgloss.JoinHorizontal(lipgloss.Center, brand, "  ", title)
	if len(h.breadcrumb) == 0 {
		return row
	}
	crumb := h.theme.BreadcrumbStyle.Render(strings.Join(h.breadcrumb, " "+theme.GlyphArrow+" "))
	return row + "\n" + crumb
}
