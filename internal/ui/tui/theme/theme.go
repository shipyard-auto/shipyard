package theme

import (
	"math"
	"os"

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

	MenuIndent = 2
	GapSmall   = 1
	GapMedium  = 2
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
		Primary:      lipgloss.AdaptiveColor{Light: "#124E89", Dark: "#3A7DCE"},
		Accent:       lipgloss.AdaptiveColor{Light: "#0087A8", Dark: "#35D4FF"},
		Success:      lipgloss.AdaptiveColor{Light: "#0D7A43", Dark: "#43D17D"},
		Warning:      lipgloss.AdaptiveColor{Light: "#A65F00", Dark: "#FFB347"},
		Danger:       lipgloss.AdaptiveColor{Light: "#B31B1B", Dark: "#FF6B6B"},
		Muted:        lipgloss.AdaptiveColor{Light: "#6F7782", Dark: "#8B949E"},
		Surface:      lipgloss.AdaptiveColor{Light: "#F7F9FC", Dark: "#1C232D"},
		SurfaceAlt:   lipgloss.AdaptiveColor{Light: "#EDF2F7", Dark: "#232C38"},
		Text:         lipgloss.AdaptiveColor{Light: "#102A43", Dark: "#F2F5F9"},
		TextInverse:  lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#0F1720"},
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
	t.PanelStyle = lipgloss.NewStyle().Foreground(t.Text).Background(t.Surface).Border(lipgloss.RoundedBorder()).BorderForeground(t.Primary).Padding(1, 2)
	t.FocusedPanelStyle = lipgloss.NewStyle().Foreground(t.Text).Background(t.Surface).Border(lipgloss.RoundedBorder()).BorderForeground(t.Accent).Padding(1, 2)
	t.MenuItemStyle = lipgloss.NewStyle().Foreground(t.Text)
	t.MenuItemSelectedStyle = lipgloss.NewStyle().Foreground(t.Text).Bold(true)
	t.InputStyle = lipgloss.NewStyle().Foreground(t.Text).Border(lipgloss.RoundedBorder()).BorderForeground(t.Primary).Padding(0, 1)
	t.InputFocusedStyle = lipgloss.NewStyle().Foreground(t.Text).Border(lipgloss.RoundedBorder()).BorderForeground(t.Accent).Padding(0, 1)
	t.LabelStyle = lipgloss.NewStyle().Foreground(t.Muted).Bold(true)
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
	style := t.KeyHintStyle
	if t.ColorEnabled {
		return lipgloss.JoinHorizontal(lipgloss.Left,
			lipgloss.NewStyle().Foreground(t.Accent).Bold(true).Render("["+key+"]"),
			" ",
			style.Render(label),
		)
	}
	return "[" + key + "] " + label
}
