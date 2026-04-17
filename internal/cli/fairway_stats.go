package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui"
)

type fairwayStatsClient interface {
	Close() error
	Stats(ctx context.Context) (fairwayctl.StatsSnapshot, error)
}

type fairwayStatsDeps struct {
	version    string
	socketPath string
	dial       func(context.Context, fairwayctl.Opts) (fairwayStatsClient, error)
}

func newFairwayStatsCmd() *cobra.Command {
	return newFairwayStatsCmdWith(fairwayStatsDeps{})
}

func newFairwayStatsCmdWith(deps fairwayStatsDeps) *cobra.Command {
	var jsonOutput bool
	var byRoute bool
	var byStatus bool

	cmd := &cobra.Command{
		Use:           "stats",
		Short:         "Show fairway request statistics from the daemon",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			deps = deps.withDefaults()
			client, err := deps.dial(cmd.Context(), fairwayctl.Opts{
				SocketPath: deps.socketPath,
				Version:    deps.version,
			})
			if err != nil {
				if errors.Is(err, fairwayctl.ErrDaemonNotRunning) {
					return fmt.Errorf("fairway daemon is offline; start it and retry")
				}
				return err
			}
			defer client.Close() //nolint:errcheck

			snap, err := client.Stats(cmd.Context())
			if err != nil {
				return err
			}
			switch {
			case jsonOutput:
				return renderFairwayStatsJSON(cmd.OutOrStdout(), snap)
			case byRoute:
				renderFairwayStatsByRoute(cmd.OutOrStdout(), snap)
			case byStatus:
				renderFairwayStatsByStatus(cmd.OutOrStdout(), snap)
			default:
				renderFairwayStatsSummary(cmd.OutOrStdout(), snap)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the full stats snapshot as JSON")
	cmd.Flags().BoolVar(&byRoute, "by-route", false, "Print full per-route counters")
	cmd.Flags().BoolVar(&byStatus, "by-status", false, "Print status-code distribution")
	return cmd
}

func (d fairwayStatsDeps) withDefaults() fairwayStatsDeps {
	if d.version == "" {
		d.version = app.Version
	}
	if d.socketPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			d.socketPath = filepath.Join(home, ".shipyard", "run", "fairway.sock")
		}
	}
	if d.dial == nil {
		d.dial = func(ctx context.Context, opts fairwayctl.Opts) (fairwayStatsClient, error) {
			return fairwayctl.Dial(ctx, opts)
		}
	}
	return d
}

func renderFairwayStatsJSON(w interface{ Write([]byte) (int, error) }, snap fairwayctl.StatsSnapshot) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(snap)
}

func renderFairwayStatsSummary(w interface{ Write([]byte) (int, error) }, snap fairwayctl.StatsSnapshot) {
	totalErrors := int64(0)
	type routeRow struct {
		path   string
		count  int64
		errors int64
	}
	rows := make([]routeRow, 0, len(snap.ByRoute))
	for path, stats := range snap.ByRoute {
		totalErrors += stats.ErrCount
		rows = append(rows, routeRow{path: path, count: stats.Count, errors: stats.ErrCount})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count == rows[j].count {
			return rows[i].path < rows[j].path
		}
		return rows[i].count > rows[j].count
	})
	ui.Printf(w, "%s\n", ui.SectionTitle("Fairway Stats"))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	errorRate := 0.0
	if snap.Total > 0 {
		errorRate = float64(totalErrors) / float64(snap.Total) * 100
	}
	errorRateLabel := fmt.Sprintf("%.1f%%", errorRate)
	if errorRate > 5 {
		errorRateLabel = ui.Paint(errorRateLabel, ui.StyleBold, ui.StyleRed)
	}
	fmt.Fprintf(tw, "  Total:\t%s\n", formatInt(snap.Total))
	fmt.Fprintf(tw, "  Errors:\t%s\n", formatInt(totalErrors))
	fmt.Fprintf(tw, "  Error rate:\t%s\n", errorRateLabel)
	_ = tw.Flush()

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Top Routes"))
	if len(rows) == 0 {
		ui.Printf(w, "  %s\n", ui.Muted("No route traffic yet."))
		return
	}
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  PATH\tCALLS\tERRORS")
	limit := 5
	if len(rows) < limit {
		limit = len(rows)
	}
	for _, row := range rows[:limit] {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", row.path, formatInt(row.count), formatInt(row.errors))
	}
	_ = tw.Flush()
}

func renderFairwayStatsByRoute(w interface{ Write([]byte) (int, error) }, snap fairwayctl.StatsSnapshot) {
	type row struct {
		path   string
		count  int64
		errors int64
		lastAt string
	}
	rows := make([]row, 0, len(snap.ByRoute))
	for path, stats := range snap.ByRoute {
		lastAt := "-"
		if !stats.LastAt.IsZero() {
			lastAt = stats.LastAt.Format(time.RFC3339)
		}
		rows = append(rows, row{path: path, count: stats.Count, errors: stats.ErrCount, lastAt: lastAt})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].path < rows[j].path })
	if len(rows) == 0 {
		ui.Printf(w, "%s\n", ui.Muted("No route traffic yet."))
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tCALLS\tERRORS\tLAST AT")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.path, formatInt(row.count), formatInt(row.errors), row.lastAt)
	}
	_ = tw.Flush()
}

func renderFairwayStatsByStatus(w interface{ Write([]byte) (int, error) }, snap fairwayctl.StatsSnapshot) {
	codes := make([]int, 0, len(snap.ByStatus))
	for code := range snap.ByStatus {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	if len(codes) == 0 {
		ui.Printf(w, "%s\n", ui.Muted("No status samples yet."))
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tCOUNT")
	for _, code := range codes {
		label := strconv.Itoa(code)
		switch {
		case code >= 200 && code < 300:
			label = ui.Paint(label, ui.StyleBold, ui.StyleGreen)
		case code >= 400 && code < 500:
			label = ui.Paint(label, ui.StyleBold, ui.StyleYellow)
		case code >= 500:
			label = ui.Paint(label, ui.StyleBold, ui.StyleRed)
		}
		fmt.Fprintf(tw, "%s\t%s\n", label, formatInt(snap.ByStatus[code]))
	}
	_ = tw.Flush()
}
