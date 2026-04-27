package cli

import (
	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/logs"
)

// newFairwayLogsCmd is a thin wrapper around `shipyard logs show/tail` that
// pre-selects the fairway source so legacy callers keep their muscle memory.
// All formatting and reading lives in internal/logs/{query,render}.go.
func newFairwayLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Inspect HTTP request logs from the fairway gateway",
		Long: `Reads JSONL access and event logs written by the fairway daemon to
~/.shipyard/logs/fairway/. Supports the same --trace, --id, and --level
filters as "shipyard logs show". This command group is a focused alias
that pre-selects the fairway source.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newFairwayLogsShowCmd())
	cmd.AddCommand(newFairwayLogsTailCmd())
	return cmd
}

func newFairwayLogsShowCmd() *cobra.Command {
	opts := logShowOptions{
		filter: logFilterFlags{sources: []string{logs.SourceFairway}},
		limit:  50,
	}
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print recent HTTP request logs from the fairway gateway",
		Long: `Reads the most recent JSONL entries from ~/.shipyard/logs/fairway/. Use
--limit to control how many entries are shown, --trace to follow a single
request, or --level to filter by severity. Add --json to emit raw JSONL
instead of pretty output.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(opts.filter.sources) == 0 {
				opts.filter.sources = []string{logs.SourceFairway}
			}
			return runLogsShow(cmd, opts)
		},
	}
	addLogFilterFlags(cmd, &opts.filter)
	cmd.Flags().IntVar(&opts.limit, "limit", 50, "Maximum number of log entries to show")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print raw JSONL lines instead of pretty-formatted output")
	return cmd
}

func newFairwayLogsTailCmd() *cobra.Command {
	opts := logShowOptions{
		filter: logFilterFlags{sources: []string{logs.SourceFairway}},
	}
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Stream live HTTP request logs from the fairway gateway",
		Long: `Follows ~/.shipyard/logs/fairway/ and prints new JSONL entries as they
arrive. Press Ctrl+C to stop. Supports --trace, --id, and --level filters
to narrow the stream. Add --json to emit raw JSONL instead of pretty output.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(opts.filter.sources) == 0 {
				opts.filter.sources = []string{logs.SourceFairway}
			}
			return runLogsTail(cmd, opts)
		},
	}
	addLogFilterFlags(cmd, &opts.filter)
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print raw JSONL lines instead of pretty-formatted output")
	return cmd
}
