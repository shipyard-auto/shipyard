package main

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

// ── sent ─────────────────────────────────────────────────────────────────

func runSent(ctx context.Context, e env, args []string) error {
	fs := newFlags("sent", e)
	sinceFlag := fs.String("since", "24h", "how far back to list (e.g. 2h, 7d; the log keeps 30d)")
	client := fs.String("client", "", `only sends requested by this fairway client ("local" = CLI and crew tools)`)
	limit := fs.Int("limit", 100, "newest entries to show (0 = all)")
	asJSON := fs.Bool("json", false, "emit JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fmt.Errorf("usage: sent [--since 24h] [--client ID|local] [--limit 100] [--json] (see '%s help sent')", progName())
	}
	since, err := wa.ParseDuration(*sinceFlag)
	if err != nil {
		return err
	}
	arch, err := openArchive(ctx, e)
	if err != nil {
		return err
	}
	defer func() { _ = arch.Close() }()
	rows, err := arch.ListSent(ctx, time.Now().Add(-since), *client, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		if rows == nil {
			rows = []archive.SentEntry{}
		}
		return writeJSON(e.stdout, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintf(e.stdout, "Nothing sent in the last %s.\n", wa.FormatDuration(since))
		return nil
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tCHAT\tKIND\tCHARS\tBY\tKEY")
	for _, r := range rows {
		by := r.Client
		if by == "" {
			by = "local"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", r.Timestamp.Local().Format("2006-01-02 15:04:05"), r.ChatName, r.Kind, r.Chars, by, r.IdempotencyKey)
	}
	return tw.Flush()
}
