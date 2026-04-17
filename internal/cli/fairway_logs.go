package cli

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
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/ui"
)

type fairwayLogEvent struct {
	Timestamp string              `json:"timestamp"`
	Source    string              `json:"source"`
	Level     string              `json:"level"`
	Event     string              `json:"event"`
	Message   string              `json:"message"`
	Data      fairwayLogEventData `json:"data"`
	raw       []byte
}

type fairwayLogEventData struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"durationMs"`
	RemoteAddr string `json:"remoteAddr"`
	Action     string `json:"action"`
	Target     string `json:"target"`
	ExitCode   int    `json:"exitCode"`
	AuthType   string `json:"authType"`
	AuthResult string `json:"authResult"`
	Truncated  bool   `json:"truncated"`
}

type logFile interface {
	io.Reader
	io.Seeker
	io.Closer
}

type logFS interface {
	Open(name string) (logFile, error)
	Stat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
}

type osLogFS struct{}

func (osLogFS) Open(name string) (logFile, error)     { return os.Open(name) }
func (osLogFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }
func (osLogFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(name)
}

type fairwayLogReader struct {
	dir   string
	now   func() time.Time
	fs    logFS
	sleep func(time.Duration)
}

type fairwayLogsDeps struct {
	dir    string
	reader *fairwayLogReader
	now    func() time.Time
}

type fairwayLogsOptions struct {
	date   string
	follow bool
	since  time.Duration
	level  string
	json   bool
	pretty bool
}

func newFairwayLogsCmd() *cobra.Command {
	return newFairwayLogsCmdWith(fairwayLogsDeps{})
}

func newFairwayLogsCmdWith(deps fairwayLogsDeps) *cobra.Command {
	opts := fairwayLogsOptions{pretty: true}
	cmd := &cobra.Command{
		Use:           "logs",
		Short:         "Show fairway request logs from JSONL files",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := deps.withDefaults().reader
			logFiles, err := resolveFairwayLogFiles(reader, opts)
			if err != nil {
				return err
			}
			cutoff := time.Time{}
			if opts.since > 0 {
				cutoff = reader.now().Add(-opts.since)
			}
			if opts.follow {
				return followFairwayLogs(cmd.Context(), reader, logFiles, cutoff, opts.level, opts.json, opts.pretty, cmd.OutOrStdout())
			}
			return readFairwayLogs(reader, logFiles, cutoff, opts.level, opts.json, opts.pretty, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.date, "date", "", "Read a specific log file date (YYYY-MM-DD)")
	cmd.Flags().BoolVar(&opts.follow, "follow", false, "Follow new appended log lines")
	cmd.Flags().DurationVar(&opts.since, "since", 0, "Only include entries newer than the given duration")
	cmd.Flags().StringVar(&opts.level, "level", "", "Filter by level: info, warn, error")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print raw JSONL lines")
	cmd.Flags().BoolVar(&opts.pretty, "pretty", true, "Print human-readable log lines")
	return cmd
}

func (d fairwayLogsDeps) withDefaults() fairwayLogsDeps {
	if d.now == nil {
		d.now = time.Now
	}
	if d.dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			d.dir = filepath.Join(home, ".shipyard", "logs", "fairway")
		}
	}
	if d.reader == nil {
		d.reader = &fairwayLogReader{
			dir:   d.dir,
			now:   d.now,
			fs:    osLogFS{},
			sleep: time.Sleep,
		}
	}
	return d
}

func resolveFairwayLogFiles(reader *fairwayLogReader, opts fairwayLogsOptions) ([]string, error) {
	if opts.date != "" {
		path := filepath.Join(reader.dir, opts.date+".jsonl")
		if _, err := reader.fs.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("fairway logs file %s does not exist", path)
			}
			return nil, err
		}
		return []string{path}, nil
	}
	if opts.since > 0 {
		cutoff := reader.now().Add(-opts.since)
		entries, err := reader.fs.ReadDir(reader.dir)
		if err != nil {
			return nil, err
		}
		var files []string
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			if info.ModTime().Before(cutoff) {
				continue
			}
			files = append(files, filepath.Join(reader.dir, entry.Name()))
		}
		sort.Strings(files)
		if len(files) == 0 {
			return []string{}, nil
		}
		return files, nil
	}
	return []string{filepath.Join(reader.dir, reader.now().Format("2006-01-02")+".jsonl")}, nil
}

func readFairwayLogs(reader *fairwayLogReader, files []string, cutoff time.Time, level string, jsonOutput, pretty bool, w io.Writer) error {
	for _, path := range files {
		if err := streamFairwayLogFile(reader, path, cutoff, level, jsonOutput, pretty, w); err != nil {
			return err
		}
	}
	return nil
}

func followFairwayLogs(ctx context.Context, reader *fairwayLogReader, files []string, cutoff time.Time, level string, jsonOutput, pretty bool, w io.Writer) error {
	if len(files) == 0 {
		files = []string{filepath.Join(reader.dir, reader.now().Format("2006-01-02")+".jsonl")}
	}
	currentPath := files[len(files)-1]
	offset := int64(0)
	autoRotate := len(files) == 1 && currentPath == filepath.Join(reader.dir, reader.now().Format("2006-01-02")+".jsonl")
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		nextDefault := filepath.Join(reader.dir, reader.now().Format("2006-01-02")+".jsonl")
		if autoRotate && cutoff.IsZero() && currentPath != nextDefault {
			currentPath = nextDefault
			offset = 0
		}
		nextOffset, err := streamFairwayLogChunk(reader, currentPath, offset, cutoff, level, jsonOutput, pretty, w)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				reader.sleep(500 * time.Millisecond)
				continue
			}
			return err
		}
		offset = nextOffset
		reader.sleep(500 * time.Millisecond)
	}
}

func streamFairwayLogFile(reader *fairwayLogReader, path string, cutoff time.Time, level string, jsonOutput, pretty bool, w io.Writer) error {
	file, err := reader.fs.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("fairway logs file %s does not exist", path)
		}
		return err
	}
	defer file.Close() //nolint:errcheck

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		event, ok, err := parseFairwayLogLine(line, cutoff, level)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if jsonOutput {
			if _, err := fmt.Fprintln(w, string(event.raw)); err != nil {
				return err
			}
			continue
		}
		if pretty {
			renderFairwayLogPretty(w, event)
		}
	}
	return scanner.Err()
}

func streamFairwayLogChunk(reader *fairwayLogReader, path string, offset int64, cutoff time.Time, level string, jsonOutput, pretty bool, w io.Writer) (int64, error) {
	file, err := reader.fs.Open(path)
	if err != nil {
		return offset, err
	}
	defer file.Close() //nolint:errcheck
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		event, ok, err := parseFairwayLogLine(line, cutoff, level)
		if err != nil {
			return offset, err
		}
		if !ok {
			continue
		}
		if jsonOutput {
			if _, err := fmt.Fprintln(w, string(event.raw)); err != nil {
				return offset, err
			}
			continue
		}
		if pretty {
			renderFairwayLogPretty(w, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return offset, err
	}
	nextOffset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return offset, err
	}
	return nextOffset, nil
}

func parseFairwayLogLine(line []byte, cutoff time.Time, level string) (fairwayLogEvent, bool, error) {
	var event fairwayLogEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return fairwayLogEvent{}, false, fmt.Errorf("decode fairway log line: %w", err)
	}
	event.raw = line
	if level != "" && !strings.EqualFold(event.Level, level) {
		return fairwayLogEvent{}, false, nil
	}
	if !cutoff.IsZero() {
		ts, err := time.Parse(time.RFC3339, event.Timestamp)
		if err != nil {
			return fairwayLogEvent{}, false, fmt.Errorf("parse fairway log timestamp %q: %w", event.Timestamp, err)
		}
		if ts.Before(cutoff) {
			return fairwayLogEvent{}, false, nil
		}
	}
	return event, true, nil
}

func renderFairwayLogPretty(w io.Writer, event fairwayLogEvent) {
	status := fmt.Sprintf("%d", event.Data.Status)
	switch {
	case event.Data.Status >= 200 && event.Data.Status < 300:
		status = ui.Paint(status, ui.StyleBold, ui.StyleGreen)
	case event.Data.Status >= 400 && event.Data.Status < 500:
		status = ui.Paint(status, ui.StyleBold, ui.StyleYellow)
	case event.Data.Status >= 500:
		status = ui.Paint(status, ui.StyleBold, ui.StyleRed)
	}

	ts := event.Timestamp
	if parsed, err := time.Parse(time.RFC3339, event.Timestamp); err == nil {
		ts = parsed.Format("2006-01-02 15:04:05")
	}
	action := "-"
	if event.Data.Action != "" {
		action = event.Data.Action
		if event.Data.Target != "" && event.Data.ExitCode >= 0 {
			action += ":" + event.Data.Target
		}
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s %s\t%dms\t%s\t%s\n",
		ts,
		status,
		event.Data.Method,
		event.Data.Path,
		event.Data.DurationMs,
		event.Data.AuthType,
		action,
	)
	_ = tw.Flush()
}
