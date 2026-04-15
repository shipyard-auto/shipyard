package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Adaptive palette: works on light and dark terminals.
var (
	splashPrimary = lipgloss.AdaptiveColor{Light: "#1E40AF", Dark: "#60A5FA"}
	splashAccent  = lipgloss.AdaptiveColor{Light: "#0891B2", Dark: "#22D3EE"}
	splashSails   = lipgloss.AdaptiveColor{Light: "#3B82F6", Dark: "#93C5FD"}
	splashWaves   = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"}
	splashMuted   = lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"}
	splashTitle   = lipgloss.AdaptiveColor{Light: "#0F172A", Dark: "#F8FAFC"}
)

var (
	mastStyle    = lipgloss.NewStyle().Foreground(splashAccent).Bold(true)
	sailStyle    = lipgloss.NewStyle().Foreground(splashSails).Bold(true)
	hullStyle    = lipgloss.NewStyle().Foreground(splashPrimary).Bold(true)
	waterStyle   = lipgloss.NewStyle().Foreground(splashPrimary)
	waveStyle    = lipgloss.NewStyle().Foreground(splashWaves)
	titleStyle   = lipgloss.NewStyle().Foreground(splashTitle).Bold(true)
	taglineStyle = lipgloss.NewStyle().Foreground(splashMuted).Italic(true)
	splashFrame  = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(splashPrimary).
			Padding(1, 4)
)

// HelpSplash returns the framed Shipyard splash banner.
func HelpSplash() string {
	ship := strings.Join([]string{
		mastStyle.Render(`           |    |    |           `),
		sailStyle.Render(`          )_)  )_)  )_)          `),
		sailStyle.Render(`         )___))___))___)\        `),
		sailStyle.Render(`        )____)____)_____)\\      `),
		hullStyle.Render(`      _____|____|____|____\\__   `),
		waterStyle.Render(`-----\                       /-----`),
		waveStyle.Render(`  ^^^^ ^^^^^^^^^^^^^^^^^^^ ^^^^   `),
	}, "\n")

	title := titleStyle.Render("SHIPYARD :: TERMINAL DRYDOCK")
	tagline := taglineStyle.Render("Build, install and service your fleet")

	body := lipgloss.JoinVertical(
		lipgloss.Center,
		ship,
		"",
		title,
		tagline,
	)

	return splashFrame.Render(body)
}
