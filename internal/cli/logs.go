package cli

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/ui"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/logwiz"
	tuitype "github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Inspect Shipyard logs",
		Long: strings.Join([]string{
			"Inspect local Shipyard logs stored in ~/.shipyard/logs as JSONL files.",
			"",
			"The first version is focused on cron events, but the log model is shared",
			"so future services and agents can plug into the same system.",
		}, "\n"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newLogsListCmd())
	cmd.AddCommand(newLogsShowCmd())
	cmd.AddCommand(newLogsTailCmd())
	cmd.AddCommand(newLogsPruneCmd())
	cmd.AddCommand(newLogsConfigCmd())

	return cmd
}

func newLogsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List known Shipyard log sources",
		RunE: func(cmd *cobra.Command, _ []string) error {
			service, err := logs.NewService()
			if err != nil {
				return err
			}
			sources, err := service.ListSources()
			if err != nil {
				return err
			}

			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.SectionTitle("Shipyard Log Sources"))
			ui.Printf(cmd.OutOrStdout(), "%s\n\n", ui.Muted("Local JSONL log sources currently present on disk."))
			if len(sources) == 0 {
				ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("No log sources found yet."))
				return nil
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "Source\tFiles\tSize(bytes)\tNewest")
			for _, source := range sources {
				fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n", source.Source, source.Files, source.SizeBytes, source.NewestFile)
			}
			tw.Flush()
			return nil
		},
	}
}

func newLogsShowCmd() *cobra.Command {
	var source, entityID, level string
	var limit int

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show recent log entries",
		Example: strings.Join([]string{
			"shipyard logs show --source cron",
			"shipyard logs show --source cron --id AB12CD --limit 20",
		}, "\n"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			service, err := logs.NewService()
			if err != nil {
				return err
			}
			events, err := service.Query(logs.Query{
				Source: source,
				Entity: strings.ToUpper(strings.TrimSpace(entityID)),
				Level:  level,
				Limit:  limit,
			})
			if err != nil {
				return err
			}

			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.SectionTitle("Shipyard Logs"))
			if len(events) == 0 {
				ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Muted("No matching log events found."))
				return nil
			}
			for _, event := range events {
				ui.Printf(cmd.OutOrStdout(), "%s\n", logsLine(event))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", logs.DefaultSourceCron, "Log source to query")
	cmd.Flags().StringVar(&entityID, "id", "", "Entity ID filter")
	cmd.Flags().StringVar(&level, "level", "", "Log level filter")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of log entries to show")
	return cmd
}

func newLogsTailCmd() *cobra.Command {
	var source, entityID, level string

	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Tail live Shipyard logs",
		Example: strings.Join([]string{
			"shipyard logs tail --source cron",
			"shipyard logs tail --source cron --id AB12CD",
		}, "\n"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			service, err := logs.NewService()
			if err != nil {
				return err
			}
			stop := make(chan struct{})
			return service.Tail(logs.Query{
				Source: source,
				Entity: strings.ToUpper(strings.TrimSpace(entityID)),
				Level:  level,
			}, cmd.OutOrStdout(), stop)
		},
	}
	cmd.Flags().StringVar(&source, "source", logs.DefaultSourceCron, "Log source to tail")
	cmd.Flags().StringVar(&entityID, "id", "", "Entity ID filter")
	cmd.Flags().StringVar(&level, "level", "", "Log level filter")
	return cmd
}

func newLogsPruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Delete logs older than the retention policy",
		RunE: func(cmd *cobra.Command, _ []string) error {
			service, err := logs.NewService()
			if err != nil {
				return err
			}
			result, err := service.Prune()
			if err != nil {
				return err
			}
			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("Shipyard logs pruned."))
			ui.Printf(cmd.OutOrStdout(), "Deleted files: %d\n", result.DeletedFiles)
			ui.Printf(cmd.OutOrStdout(), "Freed bytes:   %d\n", result.FreedBytes)
			return nil
		},
	}
}

func newLogsConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or update logs configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && tuitype.IsInteractive(tuitype.StdinFD()) {
				service, err := logs.NewService()
				if err != nil {
					return err
				}
				if _, err := tea.NewProgram(logwiz.NewRoot(service), tea.WithAltScreen()).Run(); err != nil {
					return err
				}
				return nil
			}
			service, err := logs.NewService()
			if err != nil {
				return err
			}
			cfg, err := service.LoadConfig()
			if err != nil {
				return err
			}
			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.SectionTitle("Shipyard Logs Config"))
			ui.Printf(cmd.OutOrStdout(), "retentionDays: %d\n", cfg.RetentionDays)
			return nil
		},
	}

	setCmd := &cobra.Command{
		Use:   "set retention-days <n>",
		Short: "Update log retention days",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "retention-days" {
				return fmt.Errorf("unsupported config key: %s", args[0])
			}
			days, err := strconv.Atoi(args[1])
			if err != nil || days <= 0 {
				return fmt.Errorf("retention-days must be a positive integer")
			}
			service, err := logs.NewService()
			if err != nil {
				return err
			}
			cfg, err := service.SetRetentionDays(days)
			if err != nil {
				return err
			}
			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("Updated Shipyard logs config."))
			ui.Printf(cmd.OutOrStdout(), "retentionDays: %d\n", cfg.RetentionDays)
			return nil
		},
	}
	cmd.AddCommand(setCmd)
	return cmd
}

func logsLine(event logs.Event) string {
	return fmt.Sprintf("%s [%s] %s/%s %s", event.Timestamp.Format("2006-01-02T15:04:05Z07:00"), strings.ToUpper(event.Level), event.Source, event.EntityID, event.Message)
}
