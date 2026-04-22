package crew

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// logEntry is the decoded shape of a single JSONL log line written by crew
// workers. Unknown fields are ignored to stay tolerant of schema evolution.
type logEntry struct {
	TS      time.Time      `json:"ts"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	TraceID string         `json:"trace_id"`
	Agent   string         `json:"agent"`
	Fields  map[string]any `json:"fields"`

	raw string
}

// agentName returns the agent the entry is associated with, favoring the
// top-level `agent` field and falling back to `fields.agent`.
func (e logEntry) agentName() string {
	if e.Agent != "" {
		return e.Agent
	}
	if v, ok := e.Fields["agent"].(string); ok {
		return v
	}
	return ""
}

type logsFlags struct {
	Follow bool
	Since  time.Duration
	Tail   int
	JSON   bool
}

type logsDeps struct {
	Home   string
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
	// FollowInterval controls the poll interval in --follow mode. Zero uses a
	// 500ms default. Tests override to keep runs snappy.
	FollowInterval time.Duration
}

func (d logsDeps) withDefaults() logsDeps {
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.FollowInterval == 0 {
		d.FollowInterval = 500 * time.Millisecond
	}
	return d
}

func newLogsCmd() *cobra.Command {
	return newLogsCmdWith(logsDeps{})
}

func newLogsCmdWith(deps logsDeps) *cobra.Command {
	flags := &logsFlags{}
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show logs for a crew member",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(cmd.Context(), deps, args[0], *flags)
		},
	}
	cmd.Flags().BoolVarP(&flags.Follow, "follow", "f", false, "tail logs")
	cmd.Flags().DurationVar(&flags.Since, "since", 0, "show entries newer than duration (e.g. 1h)")
	cmd.Flags().IntVar(&flags.Tail, "tail", 100, "last N lines (0 = all)")
	cmd.Flags().BoolVar(&flags.JSON, "json", false, "emit raw JSONL without formatting")
	return cmd
}

// runLogs implements the logs subcommand by reading JSONL files under
// ~/.shipyard/logs/crew. The file format is a per-day JSONL (YYYY-MM-DD.jsonl);
// this is a transitional fallback — once the shared logs subsystem exposes
// structured filters the command should delegate to it.
func runLogs(ctx context.Context, deps logsDeps, name string, flags logsFlags) error {
	deps = deps.withDefaults()

	home := deps.Home
	if home == "" {
		h, err := shipyardHome()
		if err != nil {
			return err
		}
		home = h
	}

	logsDir := filepath.Join(home, "logs", "crew")
	files, err := listLogFiles(logsDir)
	if err != nil {
		return err
	}

	offsets := make(map[string]int64, len(files))
	entries, latestOffsets, err := readEntries(files, name, flags, deps.Now())
	if err != nil {
		return err
	}
	for k, v := range latestOffsets {
		offsets[k] = v
	}

	if flags.Tail > 0 && len(entries) > flags.Tail {
		entries = entries[len(entries)-flags.Tail:]
	}
	for _, e := range entries {
		emitLog(deps.Stdout, e, flags.JSON)
	}

	if !flags.Follow {
		return nil
	}

	ticker := time.NewTicker(deps.FollowInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		refresh, err := listLogFiles(logsDir)
		if err != nil {
			return err
		}
		var since time.Time
		if flags.Since > 0 {
			since = deps.Now().Add(-flags.Since)
		}
		for _, f := range refresh {
			prev := offsets[f]
			newEntries, newOffset, err := readSince(f, name, flags, since, prev)
			if err != nil {
				return err
			}
			offsets[f] = newOffset
			for _, e := range newEntries {
				emitLog(deps.Stdout, e, flags.JSON)
			}
		}
	}
}

// listLogFiles returns JSONL files under dir sorted by filename (chronological
// because the names embed YYYY-MM-DD). Missing dir is not an error.
func listLogFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read logs dir: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files, nil
}

// readEntries reads every file and returns entries filtered by agent + since,
// along with the byte offset reached for each file (used later by --follow).
func readEntries(files []string, agent string, flags logsFlags, now time.Time) ([]logEntry, map[string]int64, error) {
	out := []logEntry{}
	offsets := make(map[string]int64, len(files))
	since := time.Time{}
	if flags.Since > 0 {
		since = now.Add(-flags.Since)
	}
	for _, f := range files {
		entries, offset, err := readSince(f, agent, flags, since, 0)
		if err != nil {
			return nil, nil, err
		}
		offsets[f] = offset
		out = append(out, entries...)
	}
	return out, offsets, nil
}

// readSince reads a single file starting at byte offset `from`, filters by
// agent and since, and returns the decoded entries plus the offset reached.
// A zero `since` disables time filtering.
func readSince(path, agent string, flags logsFlags, since time.Time, from int64) ([]logEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, from, nil
		}
		return nil, from, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if from > 0 {
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			return nil, from, fmt.Errorf("seek %s: %w", path, err)
		}
	}

	var out []logEntry
	reader := bufio.NewReader(f)
	offset := from
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			offset += int64(len(line))
			trimmed := strings.TrimRight(string(line), "\n")
			if trimmed == "" {
				// fallthrough to EOF check
			} else {
				var e logEntry
				if jerr := json.Unmarshal([]byte(trimmed), &e); jerr != nil {
					// Skip malformed lines — a log line shouldn't break reading.
					if errors.Is(err, io.EOF) {
						break
					}
					continue
				}
				e.raw = trimmed
				if e.agentName() != agent {
					if errors.Is(err, io.EOF) {
						break
					}
					continue
				}
				if !since.IsZero() && !e.TS.IsZero() && e.TS.Before(since) {
					if errors.Is(err, io.EOF) {
						break
					}
					continue
				}
				out = append(out, e)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, offset, fmt.Errorf("read %s: %w", path, err)
		}
	}
	return out, offset, nil
}

func emitLog(w io.Writer, e logEntry, asJSON bool) {
	if asJSON {
		fmt.Fprintln(w, e.raw)
		return
	}
	ts := ""
	if !e.TS.IsZero() {
		ts = e.TS.UTC().Format(time.RFC3339)
	}
	level := e.Level
	if level == "" {
		level = "-"
	}
	trace := e.TraceID
	if trace == "" {
		if v, ok := e.Fields["trace_id"].(string); ok {
			trace = v
		}
	}
	if trace == "" {
		trace = "-"
	}
	fmt.Fprintf(w, "%s %s %s %s: %s\n", ts, level, e.agentName(), trace, e.Message)
}
