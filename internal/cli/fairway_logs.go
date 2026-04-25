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
		Short: "Show fairway request logs (alias for `shipyard logs show --source fairway`)",
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
		Short: "Show recent fairway log entries",
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
		Short: "Tail live fairway log entries",
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
