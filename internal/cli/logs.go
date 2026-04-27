package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

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
			"Sources are discovered automatically; use --source to narrow the query.",
			"All entries follow the schema-v2 flat layout (snake_case keys).",
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
		Short: "List all log sources with file count and size on disk",
		Long: `Scans ~/.shipyard/logs/ and reports each discovered source (cron, service,
fairway, crew) with its JSONL file count, total size in bytes, and the
date of the newest file. Use this to get an overview before running
show or tail.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reader, err := newLogReader()
			if err != nil {
				return err
			}
			sources, err := reader.ListSources()
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
	opts := logShowOptions{}
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print recent log entries from one or more sources",
		Long: `Reads JSONL logs under ~/.shipyard/logs/. By default merges all sources
(cron, service, fairway, crew); filter with --source. Use --trace to follow
a single request across subsystems (e.g. an HTTP call that triggered an
agent that ran a shell tool), or --id to scope to a specific entity.`,
		Example: strings.Join([]string{
			"shipyard logs show --source cron",
			"shipyard logs show --source cron --id AB12CD --limit 20",
			"shipyard logs show --trace abc123",
			"shipyard logs show --source fairway --source cron --since 1h",
		}, "\n"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLogsShow(cmd, opts)
		},
	}
	addLogFilterFlags(cmd, &opts.filter)
	cmd.Flags().IntVar(&opts.limit, "limit", 50, "Maximum number of log entries to show")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print raw JSONL lines instead of pretty-formatted output")
	return cmd
}

func newLogsTailCmd() *cobra.Command {
	opts := logShowOptions{}
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Stream live log entries as they are written",
		Long: `Follows ~/.shipyard/logs/ and prints new entries as each subsystem writes
them. Press Ctrl+C to stop. Supports the same --source, --trace, --id,
and --level filters as "shipyard logs show".`,
		Example: strings.Join([]string{
			"shipyard logs tail --source cron",
			"shipyard logs tail --source cron --id AB12CD",
			"shipyard logs tail --trace abc123",
		}, "\n"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLogsTail(cmd, opts)
		},
	}
	addLogFilterFlags(cmd, &opts.filter)
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print raw JSONL lines instead of pretty-formatted output")
	return cmd
}

// logShowOptions bundles the flags shared between show/tail. Subcommands like
// fairway logs and crew logs construct one directly to delegate to the
// shared runners.
type logShowOptions struct {
	filter logFilterFlags
	limit  int
	json   bool
}

type logFilterFlags struct {
	sources  []string
	entityID string
	traceID  string
	level    string
	since    time.Duration
}

func addLogFilterFlags(cmd *cobra.Command, f *logFilterFlags) {
	cmd.Flags().StringSliceVar(&f.sources, "source", nil, "Log source(s) to query (repeat for multiple)")
	cmd.Flags().StringVar(&f.entityID, "id", "", "Entity ID filter")
	cmd.Flags().StringVar(&f.traceID, "trace", "", "Trace ID filter")
	cmd.Flags().StringVar(&f.level, "level", "", "Log level filter")
	cmd.Flags().DurationVar(&f.since, "since", 0, "Only include entries newer than the given duration")
}

func runLogsShow(cmd *cobra.Command, opts logShowOptions) error {
	reader, err := newLogReader()
	if err != nil {
		return err
	}
	filter := opts.filter.toFilter(time.Now())
	filter.Limit = opts.limit
	records, err := reader.Query(filter)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if !opts.json {
		ui.Printf(out, "%s\n", ui.SectionTitle("Shipyard Logs"))
	}
	if len(records) == 0 {
		if !opts.json {
			ui.Printf(out, "%s\n", ui.Muted("No matching log events found."))
		}
		return nil
	}
	render := logs.RenderOptions{
		ShowSource: len(opts.filter.sources) != 1,
		ShowTrace:  true,
	}
	for _, rec := range records {
		if opts.json {
			data, err := json.Marshal(rec)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, string(data))
			continue
		}
		if err := logs.RenderPretty(out, rec, render); err != nil {
			return err
		}
	}
	return nil
}

func runLogsTail(cmd *cobra.Command, opts logShowOptions) error {
	reader, err := newLogReader()
	if err != nil {
		return err
	}
	filter := opts.filter.toFilter(time.Now())
	stop := make(chan struct{})
	go func() {
		<-cmd.Context().Done()
		close(stop)
	}()
	out := cmd.OutOrStdout()
	if opts.json {
		return reader.Tail(filter, out, stop)
	}
	// Pretty mode: tail emits raw JSONL on out — wrap into a renderer that
	// parses each line back. We intercept via an io.Writer adapter.
	pw := &prettyTailWriter{out: out, render: logs.RenderOptions{
		ShowSource: len(opts.filter.sources) != 1,
		ShowTrace:  true,
	}}
	return reader.Tail(filter, pw, stop)
}

// prettyTailWriter parses each JSONL chunk Reader.Tail emits and renders it
// in human-readable form. Tail writes one line per record.
type prettyTailWriter struct {
	out    interface{ Write(p []byte) (int, error) }
	render logs.RenderOptions
	buf    []byte
}

func (p *prettyTailWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		idx := bytesIndexByte(p.buf, '\n')
		if idx < 0 {
			break
		}
		line := p.buf[:idx]
		p.buf = p.buf[idx+1:]
		var rec logs.Record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if tsRaw, ok := extractTimestamp(line); ok {
			rec.Timestamp = tsRaw
		}
		if err := logs.RenderPretty(p.out, rec, p.render); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func bytesIndexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func extractTimestamp(line []byte) (time.Time, bool) {
	var probe struct {
		TS string `json:"ts"`
	}
	if err := json.Unmarshal(line, &probe); err != nil || probe.TS == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, probe.TS)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func (f logFilterFlags) toFilter(now time.Time) logs.Filter {
	out := logs.Filter{
		Sources:  f.sources,
		Level:    f.level,
		EntityID: strings.ToUpper(strings.TrimSpace(f.entityID)),
		TraceID:  strings.TrimSpace(f.traceID),
	}
	if f.since > 0 {
		out.Since = now.Add(-f.since)
	}
	return out
}

func newLogReader() (*logs.Reader, error) {
	_, root, err := logs.DefaultPaths()
	if err != nil {
		return nil, err
	}
	return logs.NewReader(root), nil
}

func newLogsPruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Delete log files older than the configured retention period",
		Long: `Removes JSONL log files under ~/.shipyard/logs/ that are older than the
current retention setting (default 30 days). Reports the number of files
deleted and bytes freed. Adjust the retention period with
"shipyard logs config set retention-days <n>".`,
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
		Long: `Without arguments and with a TTY, opens an interactive panel to view and
update log settings. For scripting, use the "set" subcommand, e.g.
"shipyard logs config set retention-days 30". The only configurable setting
today is retention-days, which controls how long log files are kept.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && tuitype.IsInteractive(tuitype.StdinFD()) {
				service, err := logs.NewService()
				if err != nil {
					return err
				}
				model, err := tea.NewProgram(logwiz.NewRoot(service), tea.WithAltScreen()).Run()
				if err != nil {
					return err
				}
				if finished, ok := model.(*logwiz.Root); ok && strings.TrimSpace(finished.Summary()) != "" {
					ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis(finished.Summary()))
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
