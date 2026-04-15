package theme

import (
	"math"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

const (
	GlyphBullet       = "•"
	GlyphArrow        = "›"
	GlyphCheck        = "✓"
	GlyphCross        = "✖"
	GlyphSelected     = "▍"
	GlyphBoxChecked   = "●"
	GlyphBoxUnchecked = "○"
	GlyphDot          = "·"
	GlyphLine         = "─"

	MenuIndent = 2
	GapSmall   = 1
	GapMedium  = 2

	PageGutter = 2
)

var GlyphSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type Theme struct {
	ColorEnabled bool

	Primary     lipgloss.AdaptiveColor
	Accent      lipgloss.AdaptiveColor
	Success     lipgloss.AdaptiveColor
	Warning     lipgloss.AdaptiveColor
	Danger      lipgloss.AdaptiveColor
	Muted       lipgloss.AdaptiveColor
	Surface     lipgloss.AdaptiveColor
	SurfaceAlt  lipgloss.AdaptiveColor
	Text        lipgloss.AdaptiveColor
	TextInverse lipgloss.AdaptiveColor

	TitleStyle            lipgloss.Style
	SubtitleStyle         lipgloss.Style
	BreadcrumbStyle       lipgloss.Style
	PanelStyle            lipgloss.Style
	FocusedPanelStyle     lipgloss.Style
	MenuItemStyle         lipgloss.Style
	MenuItemSelectedStyle lipgloss.Style
	InputStyle            lipgloss.Style
	InputFocusedStyle     lipgloss.Style
	LabelStyle            lipgloss.Style
	ValueStyle            lipgloss.Style
	ErrorStyle            lipgloss.Style
	HintStyle             lipgloss.Style
	SuccessStyle          lipgloss.Style
	KeyHintStyle          lipgloss.Style
}

func New() Theme {
	noColor := os.Getenv("NO_COLOR") != ""
	if noColor {
		lipgloss.SetColorProfile(termenv.Ascii)
	} else {
		lipgloss.SetColorProfile(termenv.ColorProfile())
	}

	t := Theme{
		ColorEnabled: !noColor,
		Primary:      lipgloss.AdaptiveColor{Light: "#1E40AF", Dark: "#60A5FA"},
		Accent:       lipgloss.AdaptiveColor{Light: "#0891B2", Dark: "#22D3EE"},
		Success:      lipgloss.AdaptiveColor{Light: "#047857", Dark: "#34D399"},
		Warning:      lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"},
		Danger:       lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"},
		Muted:        lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"},
		Surface:      lipgloss.AdaptiveColor{Light: "#F8FAFC", Dark: "#0F172A"},
		SurfaceAlt:   lipgloss.AdaptiveColor{Light: "#E2E8F0", Dark: "#1E293B"},
		Text:         lipgloss.AdaptiveColor{Light: "#0F172A", Dark: "#F1F5F9"},
		TextInverse:  lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#0B1220"},
	}

	if noColor {
		t.TitleStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)
		t.SubtitleStyle = lipgloss.NewStyle().Italic(true)
		t.BreadcrumbStyle = lipgloss.NewStyle()
		t.PanelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)
		t.FocusedPanelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2).Bold(true)
		t.MenuItemStyle = lipgloss.NewStyle()
		t.MenuItemSelectedStyle = lipgloss.NewStyle().Bold(true)
		t.InputStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
		t.InputFocusedStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Bold(true)
		t.LabelStyle = lipgloss.NewStyle().Faint(true)
		t.ValueStyle = lipgloss.NewStyle().Bold(true)
		t.ErrorStyle = lipgloss.NewStyle()
		t.HintStyle = lipgloss.NewStyle()
		t.SuccessStyle = lipgloss.NewStyle()
		t.KeyHintStyle = lipgloss.NewStyle()
		return t
	}

	t.TitleStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(t.Primary)
	t.SubtitleStyle = lipgloss.NewStyle().Italic(true).Foreground(t.Muted)
	t.BreadcrumbStyle = lipgloss.NewStyle().Foreground(t.Muted)
	t.PanelStyle = lipgloss.NewStyle().Foreground(t.Text).Border(lipgloss.RoundedBorder()).BorderForeground(t.Primary).Padding(1, 2)
	t.FocusedPanelStyle = lipgloss.NewStyle().Foreground(t.Text).Border(lipgloss.RoundedBorder()).BorderForeground(t.Accent).Padding(1, 2)
	t.MenuItemStyle = lipgloss.NewStyle().Foreground(t.Text)
	t.MenuItemSelectedStyle = lipgloss.NewStyle().Foreground(t.Accent).Bold(true)
	t.InputStyle = lipgloss.NewStyle().Foreground(t.Text).Border(lipgloss.RoundedBorder()).BorderForeground(t.Muted).Padding(0, 1)
	t.InputFocusedStyle = lipgloss.NewStyle().Foreground(t.Text).Border(lipgloss.RoundedBorder()).BorderForeground(t.Accent).Padding(0, 1)
	t.LabelStyle = lipgloss.NewStyle().Foreground(t.Muted)
	t.ValueStyle = lipgloss.NewStyle().Foreground(t.Text).Bold(true)
	t.ErrorStyle = lipgloss.NewStyle().Foreground(t.Danger)
	t.HintStyle = lipgloss.NewStyle().Foreground(t.Muted)
	t.SuccessStyle = lipgloss.NewStyle().Foreground(t.Success)
	t.KeyHintStyle = lipgloss.NewStyle().Foreground(t.Muted)
	return t
}

func (t Theme) ContentWidth(terminalWidth int) int {
	if terminalWidth <= 0 {
		return 80
	}
	width := int(math.Max(60, math.Min(float64(terminalWidth-4), 100)))
	return width
}

func (t Theme) CheckboxChecked() string {
	return GlyphBoxChecked + " "
}

func (t Theme) CheckboxUnchecked() string {
	return GlyphBoxUnchecked + " "
}

func (t Theme) RenderError(text string) string {
	return t.ErrorStyle.Render(GlyphCross + " " + text)
}

func (t Theme) RenderHint(text string) string {
	return t.HintStyle.Render(GlyphArrow + " " + text)
}

func (t Theme) RenderSuccess(text string) string {
	return t.SuccessStyle.Render(GlyphCheck + " " + text)
}

func (t Theme) RenderKeyHint(key, label string) string {
	if t.ColorEnabled {
		return lipgloss.JoinHorizontal(lipgloss.Left,
			lipgloss.NewStyle().Foreground(t.Accent).Bold(true).Render("["+key+"]"),
			" ",
			t.KeyHintStyle.Render(label),
		)
	}
	return "[" + key + "] " + label
}

// Pill renders a borderless badge ` text ` with background tint.
// Variants: "muted" (default), "accent", "success", "warning", "danger".
func (t Theme) Pill(text, variant string) string {
	if !t.ColorEnabled {
		return "[" + text + "]"
	}
	style := lipgloss.NewStyle().
		Padding(0, 1).
		Foreground(t.TextInverse).
		Bold(true)

	switch variant {
	case "accent":
		style = style.Background(t.Accent)
	case "success":
		style = style.Background(t.Success)
	case "warning":
		style = style.Background(t.Warning)
	case "danger":
		style = style.Background(t.Danger)
	default:
		style = style.Background(t.Muted)
	}
	return style.Render(text)
}

// Divider renders a horizontal rule.
func (t Theme) Divider(width int) string {
	if width <= 0 {
		width = 40
	}
	line := strings.Repeat(GlyphLine, width)
	if !t.ColorEnabled {
		return line
	}
	return lipgloss.NewStyle().Foreground(t.SurfaceAlt).Render(line)
}

// Brand renders the compact `⛵ SHIPYARD` brand mark.
func (t Theme) Brand() string {
	if !t.ColorEnabled {
		return "⛵ SHIPYARD"
	}
	return lipgloss.JoinHorizontal(lipgloss.Center,
		lipgloss.NewStyle().Foreground(t.Accent).Render("⛵"),
		" ",
		lipgloss.NewStyle().Foreground(t.Primary).Bold(true).Render("SHIPYARD"),
	)
}

// PageFrame wraps content in the standard page chrome (gutter padding).
func (t Theme) PageFrame() lipgloss.Style {
	return lipgloss.NewStyle().Padding(1, PageGutter)
}
