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

type Service struct {
	ConfigStore ConfigStore
	RootDir     string
	Now         func() time.Time
}

func NewService() (Service, error) {
	configStore, err := NewConfigStore()
	if err != nil {
		return Service{}, err
	}
	_, rootDir, err := DefaultPaths()
	if err != nil {
		return Service{}, err
	}
	return Service{
		ConfigStore: configStore,
		RootDir:     rootDir,
		Now:         time.Now,
	}, nil
}

func (s Service) LoadConfig() (Config, error) {
	return s.ConfigStore.Load()
}

func (s Service) SetRetentionDays(days int) (Config, error) {
	cfg := Config{RetentionDays: days}
	if err := s.ConfigStore.Save(cfg); err != nil {
		return Config{}, err
	}
	return s.ConfigStore.Load()
}

func (s Service) Write(event Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = s.Now().UTC()
	} else {
		event.Timestamp = event.Timestamp.UTC()
	}

	path := dailyLogPath(s.RootDir, event.Source, event.Timestamp)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer file.Close()

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal log event: %w", err)
	}

	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append log event: %w", err)
	}
	return nil
}

func (s Service) ListSources() ([]SourceSummary, error) {
	if err := os.MkdirAll(s.RootDir, 0o755); err != nil {
		return nil, fmt.Errorf("create logs root directory: %w", err)
	}

	entries, err := os.ReadDir(s.RootDir)
	if err != nil {
		return nil, fmt.Errorf("read logs root directory: %w", err)
	}

	summaries := make([]SourceSummary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dir := filepath.Join(s.RootDir, entry.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read source log directory: %w", err)
		}

		var summary SourceSummary
		summary.Source = entry.Name()
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
				continue
			}
			info, err := file.Info()
			if err != nil {
				return nil, fmt.Errorf("inspect log file: %w", err)
			}
			summary.Files++
			summary.SizeBytes += info.Size()
			if file.Name() > summary.NewestFile {
				summary.NewestFile = file.Name()
			}
		}
		summaries = append(summaries, summary)
	}

	slices.SortFunc(summaries, func(a, b SourceSummary) int {
		return strings.Compare(a.Source, b.Source)
	})

	return summaries, nil
}

func (s Service) Query(query Query) ([]Event, error) {
	source := query.Source
	if source == "" {
		source = DefaultSourceCron
	}
	dir := sourceDir(s.RootDir, source)
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Event{}, nil
		}
		return nil, fmt.Errorf("read log source directory: %w", err)
	}

	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}

	names := make([]string, 0, len(files))
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".jsonl") {
			names = append(names, file.Name())
		}
	}
	slices.Sort(names)
	slices.Reverse(names)

	result := make([]Event, 0, limit)
	for _, name := range names {
		path := filepath.Join(dir, name)
		events, err := readEvents(path)
		if err != nil {
			return nil, err
		}
		for i := len(events) - 1; i >= 0; i-- {
			event := events[i]
			if !matchesQuery(event, query) {
				continue
			}
			result = append(result, event)
			if len(result) >= limit {
				return result, nil
			}
		}
	}

	return result, nil
}

func (s Service) Tail(query Query, out io.Writer, stop <-chan struct{}) error {
	source := query.Source
	if source == "" {
		source = DefaultSourceCron
	}

	lastCount := 0
	for {
		select {
		case <-stop:
			return nil
		default:
		}

		events, err := s.Query(Query{
			Source: source,
			Entity: query.Entity,
			Level:  query.Level,
			Limit:  200,
		})
		if err != nil {
			return err
		}

		slices.Reverse(events)
		if len(events) > lastCount {
			for _, event := range events[lastCount:] {
				if _, err := fmt.Fprintln(out, formatEvent(event)); err != nil {
					return err
				}
			}
			lastCount = len(events)
		}

		time.Sleep(1 * time.Second)
	}
}

func (s Service) Prune() (PruneResult, error) {
	cfg, err := s.LoadConfig()
	if err != nil {
		return PruneResult{}, err
	}

	cutoff := s.Now().UTC().AddDate(0, 0, -cfg.RetentionDays)
	result := PruneResult{}

	if err := os.MkdirAll(s.RootDir, 0o755); err != nil {
		return PruneResult{}, fmt.Errorf("create logs root directory: %w", err)
	}

	err = filepath.WalkDir(s.RootDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}

		base := strings.TrimSuffix(d.Name(), ".jsonl")
		fileDate, err := time.Parse("2006-01-02", base)
		if err != nil {
			return nil
		}
		if !fileDate.Before(cutoff) {
			return nil
		}

		info, err := d.Info()
		if err == nil {
			result.FreedBytes += info.Size()
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		result.DeletedFiles++
		return nil
	})
	if err != nil {
		return PruneResult{}, fmt.Errorf("prune log files: %w", err)
	}

	return result, nil
}

func readEvents(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	events := []Event{}
	for scanner.Scan() {
		line := scanner.Bytes()
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("parse log event: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan log file: %w", err)
	}
	return events, nil
}

func matchesQuery(event Event, query Query) bool {
	if query.Source != "" && event.Source != query.Source {
		return false
	}
	if query.Entity != "" && event.EntityID != query.Entity {
		return false
	}
	if query.Level != "" && event.Level != query.Level {
		return false
	}
	return true
}

func formatEvent(event Event) string {
	return fmt.Sprintf("%s [%s] %s/%s %s", event.Timestamp.Format(time.RFC3339), strings.ToUpper(event.Level), event.Source, event.EntityID, event.Message)
}
