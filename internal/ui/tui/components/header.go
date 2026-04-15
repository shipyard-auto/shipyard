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
		h.width = resize.Width
	}
	return h, nil
}

func (h Header) SetWidth(width int) Header {
	h.width = width
	return h
}

func (h Header) View() string {
	brand := h.theme.Brand()
	separator := h.theme.HintStyle.Render(" " + theme.GlyphArrow + " ")
	title := h.theme.ValueStyle.Render(h.title)

	topRow := lipgloss.JoinHorizontal(lipgloss.Center, brand, separator, title)

	lines := []string{topRow}

	if len(h.breadcrumb) > 0 {
		crumb := h.theme.BreadcrumbStyle.Render(
			strings.Join(h.breadcrumb, " "+theme.GlyphArrow+" "),
		)
		lines = append(lines, crumb)
	}

	dividerWidth := h.width
	if dividerWidth <= 0 {
		dividerWidth = lipgloss.Width(topRow)
	}
	if dividerWidth > 0 {
		lines = append(lines, h.theme.Divider(dividerWidth))
	}

	return strings.Join(lines, "\n")
}
