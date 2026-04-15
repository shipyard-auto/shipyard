package cli

import (
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/ui"
)

func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           app.Name,
		Short:         "Shipyard command line tool",
		Long:          app.Description,
		SilenceUsage:  true,
		SilenceErrors: true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.InitDefaultHelpFlag()
	cmd.InitDefaultVersionFlag()
	cmd.SetHelpFunc(func(command *cobra.Command, args []string) {
		renderHelp(command.OutOrStdout(), command)
	})
	cmd.Version = app.Version

	cmd.AddCommand(newUninstallCmd())
	cmd.AddCommand(newCronCmd())
	cmd.AddCommand(newLogsCmd())
	cmd.AddCommand(newUpdateCmd())
	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newAboutCmd())
	cmd.SetHelpCommand(&cobra.Command{
		Use:   "help [command]",
		Short: "Show help for Shipyard commands",
		Long:  "Display usage, commands, flags, and examples for Shipyard.",
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			if len(args) == 0 {
				return root.Help()
			}

			helpTarget, _, err := root.Find(args)
			if err != nil {
				return err
			}

			return helpTarget.Help()
		},
	})

	return cmd
}

func renderHelp(w io.Writer, cmd *cobra.Command) {
	if cmd == cmd.Root() {
		renderRootHelp(w, cmd)
		return
	}

	ui.Printf(w, "%s\n", ui.SectionTitle(strings.ToUpper(cmd.CommandPath())))
	ui.Printf(w, "%s\n\n", ui.Muted("Shipyard command reference"))

	if cmd.Long != "" {
		for _, line := range strings.Split(strings.TrimSpace(cmd.Long), "\n") {
			ui.Printf(w, "%s\n", line)
		}
		ui.Printf(w, "\n")
	} else if cmd.Short != "" {
		ui.Printf(w, "%s\n\n", cmd.Short)
	}

	ui.Printf(w, "%s\n", ui.SectionTitle("Usage"))
	ui.Printf(w, "  %s\n", ui.Highlight(cmd.UseLine()))

	if cmd.HasAvailableLocalFlags() {
		ui.Printf(w, "\n%s\n", ui.SectionTitle("Flags"))
		ui.Printf(w, "%s", strings.TrimRight(cmd.LocalFlags().FlagUsages(), "\n"))
		ui.Printf(w, "\n")
	}

	if cmd.Example != "" {
		ui.Printf(w, "\n%s\n", ui.SectionTitle("Examples"))
		for _, line := range strings.Split(strings.TrimSpace(cmd.Example), "\n") {
			ui.Printf(w, "  %s\n", ui.Highlight(strings.TrimSpace(line)))
		}
	}

	if cmd.HasAvailableSubCommands() {
		ui.Printf(w, "\n%s\n", ui.SectionTitle("Available Commands"))
		ui.Printf(w, "  %-12s %s\n", ui.Highlight("help"), "Show help for this command")
		for _, sub := range cmd.Commands() {
			if !sub.IsAvailableCommand() || sub.IsAdditionalHelpTopicCommand() {
				continue
			}
			ui.Printf(w, "  %-12s %s\n", ui.Highlight(sub.Name()), sub.Short)
		}
	}
}

func renderRootHelp(w io.Writer, cmd *cobra.Command) {
	ui.Printf(w, "%s\n\n", ui.HelpSplash())
	ui.Printf(w, "%s\n", ui.Emphasis(app.Description))
	ui.Printf(w, "%s\n\n", ui.Muted("Professional terminal operations for Shipyard environments."))

	ui.Printf(w, "%s\n", ui.SectionTitle("Usage"))
	ui.Printf(w, "  %s\n", ui.Highlight("shipyard [command] [flags]"))

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Available Commands"))
	ui.Printf(w, "  %-12s %s\n", ui.Highlight("help"), "Show help for Shipyard commands")
	for _, sub := range cmd.Commands() {
		if !sub.IsAvailableCommand() || sub.IsAdditionalHelpTopicCommand() {
			continue
		}
		ui.Printf(w, "  %-12s %s\n", ui.Highlight(sub.Name()), sub.Short)
	}

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Flags"))
	ui.Printf(w, "%s\n", strings.TrimRight(cmd.LocalFlags().FlagUsages(), "\n"))

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Examples"))
	ui.Printf(w, "  %s\n", ui.Highlight("shipyard help"))
	ui.Printf(w, "  %s\n", ui.Highlight("shipyard cron"))
	ui.Printf(w, "  %s\n", ui.Highlight("shipyard logs"))
	ui.Printf(w, "  %s\n", ui.Highlight("shipyard update"))
	ui.Printf(w, "  %s\n", ui.Highlight("shipyard version"))
	ui.Printf(w, "  %s\n", ui.Highlight("shipyard uninstall --yes"))
}

func PrintResult(w io.Writer, format string, args ...any) {
	ui.Printf(w, format, args...)
}
