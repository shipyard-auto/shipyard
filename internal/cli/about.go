package cli

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/app"
)

var (
	aboutPrimary = lipgloss.AdaptiveColor{Light: "#1E40AF", Dark: "#60A5FA"}
	aboutAccent  = lipgloss.AdaptiveColor{Light: "#0891B2", Dark: "#22D3EE"}
	aboutMuted   = lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"}
	aboutText    = lipgloss.AdaptiveColor{Light: "#0F172A", Dark: "#F8FAFC"}
	aboutGold    = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
)

var (
	aboutFrame = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(aboutPrimary).
			Padding(1, 6)

	aboutBrand = lipgloss.NewStyle().
			Foreground(aboutPrimary).
			Bold(true)

	aboutAuthor = lipgloss.NewStyle().
			Foreground(aboutText).
			Bold(true)

	aboutQuote = lipgloss.NewStyle().
			Foreground(aboutGold).
			Italic(true)

	aboutLink = lipgloss.NewStyle().
			Foreground(aboutAccent).
			Underline(true)

	aboutLabel = lipgloss.NewStyle().
			Foreground(aboutMuted)

	aboutValue = lipgloss.NewStyle().
			Foreground(aboutText).
			Bold(true)

	aboutDivider = lipgloss.NewStyle().
			Foreground(aboutMuted).
			Render("·  ·  ·")
)

func newAboutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "about",
		Short: "About Shipyard and its author",
		Long:  "Display information about Shipyard, its author and where to find the source.",
		Run: func(cmd *cobra.Command, _ []string) {
			PrintResult(cmd.OutOrStdout(), "%s\n", renderAbout())
		},
	}
}

func renderAbout() string {
	brand := aboutBrand.Render("⛵  S H I P Y A R D")

	author := aboutAuthor.Render("Created by Fenius")
	quote := aboutQuote.Render(`"From knowledge, came freedom."`)

	github := aboutLink.Render("https://github.com/feniuspw")
	githubLine := lipgloss.JoinHorizontal(
		lipgloss.Left,
		aboutLabel.Render("GitHub  "),
		github,
	)

	versionLine := lipgloss.JoinHorizontal(
		lipgloss.Left,
		aboutLabel.Render("Version "),
		aboutValue.Render(app.Version),
	)

	body := lipgloss.JoinVertical(
		lipgloss.Center,
		brand,
		"",
		author,
		quote,
		"",
		aboutDivider,
		"",
		githubLine,
		versionLine,
	)

	return aboutFrame.Render(body)
}
