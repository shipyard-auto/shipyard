package logs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Record is one parsed JSONL log line (schema v2). Unknown fields are
// preserved in Extra so future schema additions stay readable by older
// shipyard binaries.
type Record struct {
	Timestamp time.Time `json:"-"`
	Source    string    `json:"source"`
	Level     string    `json:"level"`
	Event     string    `json:"event"`
	Message   string    `json:"message,omitempty"`

	TraceID    string `json:"trace_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	EntityType string `json:"entity_type,omitempty"`
	EntityID   string `json:"entity_id,omitempty"`
	EntityName string `json:"entity_name,omitempty"`

	DurationMs int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
	ErrorKind  string `json:"error_kind,omitempty"`

	HTTPMethod      string `json:"http_method,omitempty"`
	HTTPPath        string `json:"http_path,omitempty"`
	HTTPStatus      int    `json:"http_status,omitempty"`
	HTTPRemoteAddr  string `json:"http_remote_addr,omitempty"`
	HTTPResponseSz  int64  `json:"http_response_bytes,omitempty"`

	RouteAction   string `json:"route_action,omitempty"`
	RouteTarget   string `json:"route_target,omitempty"`
	RouteExitCode int    `json:"route_exit_code,omitempty"`

	AuthType   string `json:"auth_type,omitempty"`
	AuthResult string `json:"auth_result,omitempty"`

	ToolName     string `json:"tool_name,omitempty"`
	ToolProtocol string `json:"tool_protocol,omitempty"`
	ToolOK       bool   `json:"tool_ok,omitempty"`

	TokensInput  int `json:"tokens_input,omitempty"`
	TokensOutput int `json:"tokens_output,omitempty"`

	Hostname       string `json:"hostname,omitempty"`
	PID            int    `json:"pid,omitempty"`
	ServiceVersion string `json:"service_version,omitempty"`

	// Extra captures everything not covered by the typed fields above.
	Extra map[string]any `json:"-"`
}

// Filter narrows down which records Reader.Query returns.
type Filter struct {
	Sources  []string
	Level    string
	EntityID string
	TraceID  string
	Since    time.Time
	Limit    int
}

// Reader exposes read-side operations over a logs root. Construct with
// NewReader and reuse — it holds no open file descriptors.
type Reader struct {
	Root string
}

// NewReader constructs a Reader rooted at root.
func NewReader(root string) *Reader {
	return &Reader{Root: root}
}

// ListSources reports the directory entries under root with simple stats.
func (r *Reader) ListSources() ([]SourceSummary, error) {
	if err := os.MkdirAll(r.Root, 0o755); err != nil {
		return nil, fmt.Errorf("create logs root: %w", err)
	}
	entries, err := os.ReadDir(r.Root)
	if err != nil {
		return nil, fmt.Errorf("read logs root: %w", err)
	}
	out := make([]SourceSummary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(r.Root, entry.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read source %s: %w", entry.Name(), err)
		}
		s := SourceSummary{Source: entry.Name()}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				return nil, fmt.Errorf("stat log file: %w", err)
			}
			s.Files++
			s.SizeBytes += info.Size()
			if f.Name() > s.NewestFile {
				s.NewestFile = f.Name()
			}
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b SourceSummary) int { return strings.Compare(a.Source, b.Source) })
	return out, nil
}

// Query returns the most recent records matching f, newest first.
func (r *Reader) Query(f Filter) ([]Record, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}

	sources := f.Sources
	if len(sources) == 0 {
		sums, err := r.ListSources()
		if err != nil {
			return nil, err
		}
		sources = make([]string, 0, len(sums))
		for _, s := range sums {
			sources = append(sources, s.Source)
		}
	}

	type fileRef struct {
		path string
		day  string
	}
	var files []fileRef
	for _, src := range sources {
		dir := filepath.Join(r.Root, src)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read source %s: %w", src, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			files = append(files, fileRef{
				path: filepath.Join(dir, e.Name()),
				day:  strings.TrimSuffix(e.Name(), ".jsonl"),
			})
		}
	}
	slices.SortFunc(files, func(a, b fileRef) int { return strings.Compare(b.day, a.day) })

	result := make([]Record, 0, limit)
	for _, fr := range files {
		recs, err := readRecords(fr.path)
		if err != nil {
			return nil, err
		}
		for i := len(recs) - 1; i >= 0; i-- {
			rec := recs[i]
			if !match(rec, f) {
				continue
			}
			result = append(result, rec)
			if len(result) >= limit {
				return result, nil
			}
		}
	}
	return result, nil
}

// Tail polls the matching files and writes new records to out as plain
// JSON lines. Stop by closing stop or cancelling the surrounding context.
func (r *Reader) Tail(f Filter, out io.Writer, stop <-chan struct{}) error {
	seen := map[string]int64{}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-tick.C:
		}
		sources := f.Sources
		if len(sources) == 0 {
			sums, err := r.ListSources()
			if err != nil {
				return err
			}
			sources = make([]string, 0, len(sums))
			for _, s := range sums {
				sources = append(sources, s.Source)
			}
		}
		for _, src := range sources {
			today := time.Now().UTC().Format("2006-01-02")
			path := filepath.Join(r.Root, src, today+".jsonl")
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			off := seen[path]
			if info.Size() <= off {
				continue
			}
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			if _, err := file.Seek(off, io.SeekStart); err != nil {
				file.Close()
				continue
			}
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			for scanner.Scan() {
				line := scanner.Bytes()
				rec, err := parseRecord(line)
				if err != nil {
					continue
				}
				if !match(rec, f) {
					continue
				}
				if _, err := out.Write(append(append([]byte{}, line...), '\n')); err != nil {
					file.Close()
					return err
				}
			}
			seen[path] = info.Size()
			file.Close()
		}
	}
}

func match(rec Record, f Filter) bool {
	if f.Level != "" && !strings.EqualFold(rec.Level, f.Level) {
		return false
	}
	if f.EntityID != "" && rec.EntityID != f.EntityID {
		return false
	}
	if f.TraceID != "" && rec.TraceID != f.TraceID {
		return false
	}
	if !f.Since.IsZero() && rec.Timestamp.Before(f.Since) {
		return false
	}
	return true
}

func readRecords(path string) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	out := []Record{}
	for scanner.Scan() {
		rec, err := parseRecord(scanner.Bytes())
		if err != nil {
			// Skip malformed lines instead of failing the whole query;
			// log files can be tailed by humans/test harnesses.
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan log file: %w", err)
	}
	return out, nil
}

// parseRecord decodes a JSONL line into a Record, parsing the timestamp
// and capturing unknown keys in Extra.
func parseRecord(line []byte) (Record, error) {
	raw := map[string]any{}
	if err := json.Unmarshal(line, &raw); err != nil {
		return Record{}, fmt.Errorf("parse record: %w", err)
	}
	var rec Record
	if err := json.Unmarshal(line, &rec); err != nil {
		return Record{}, fmt.Errorf("decode record: %w", err)
	}
	if tsRaw, ok := raw[KeyTimestamp].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, tsRaw); err == nil {
			rec.Timestamp = t
		}
	}
	known := map[string]struct{}{
		"ts": {}, "source": {}, "level": {}, "event": {}, "message": {},
		"trace_id": {}, "run_id": {},
		"entity_type": {}, "entity_id": {}, "entity_name": {},
		"duration_ms": {}, "error": {}, "error_kind": {},
		"http_method": {}, "http_path": {}, "http_status": {},
		"http_remote_addr": {}, "http_response_bytes": {},
		"route_action": {}, "route_target": {}, "route_exit_code": {},
		"auth_type": {}, "auth_result": {},
		"tool_name": {}, "tool_protocol": {}, "tool_ok": {},
		"tokens_input": {}, "tokens_output": {},
		"hostname": {}, "pid": {}, "service_version": {},
	}
	for k, v := range raw {
		if _, ok := known[k]; ok {
			continue
		}
		if rec.Extra == nil {
			rec.Extra = map[string]any{}
		}
		rec.Extra[k] = v
	}
	return rec, nil
}
