package crew

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/logs"
)

// newLogsCmd is a thin wrapper around the unified `shipyard logs` reader
// that pre-selects the crew source and filters by agent name. The reading
// and rendering logic lives in internal/logs/{query,render}.go.
func newLogsCmd() *cobra.Command {
	var (
		follow  bool
		since   time.Duration
		tail    int
		level   string
		traceID string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show logs for a crew member",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reader, err := newCrewReader()
			if err != nil {
				return err
			}
			filter := logs.Filter{
				Sources: []string{logs.SourceCrew},
				Level:   level,
				TraceID: strings.TrimSpace(traceID),
			}
			if len(args) == 1 {
				filter.EntityID = strings.TrimSpace(args[0])
			}
			if since > 0 {
				filter.Since = time.Now().Add(-since)
			}
			if tail > 0 {
				filter.Limit = tail
			} else {
				filter.Limit = 100
			}
			records, err := reader.Query(filter)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			renderOpts := logs.RenderOptions{ShowTrace: true}
			for _, rec := range records {
				if asJSON {
					data, err := json.Marshal(rec)
					if err != nil {
						return err
					}
					fmt.Fprintln(out, string(data))
					continue
				}
				if err := logs.RenderPretty(out, rec, renderOpts); err != nil {
					return err
				}
			}
			if !follow {
				return nil
			}
			stop := make(chan struct{})
			go func() {
				<-cmd.Context().Done()
				close(stop)
			}()
			return reader.Tail(filter, out, stop)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "tail logs")
	cmd.Flags().DurationVar(&since, "since", 0, "show entries newer than duration (e.g. 1h)")
	cmd.Flags().IntVar(&tail, "tail", 100, "last N lines (0 = unlimited)")
	cmd.Flags().StringVar(&level, "level", "", "filter by level (info, warn, error)")
	cmd.Flags().StringVar(&traceID, "trace", "", "filter by trace id")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw JSONL without formatting")
	return cmd
}

func newCrewReader() (*logs.Reader, error) {
	_, root, err := logs.DefaultPaths()
	if err != nil {
		return nil, err
	}
	return logs.NewReader(root), nil
}
