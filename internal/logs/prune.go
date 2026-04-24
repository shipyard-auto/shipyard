package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RetentionFunc returns the retention duration for source. Implementations
// should return a non-positive duration to keep all files.
type RetentionFunc func(source string) time.Duration

// Pruner deletes log files older than a per-source retention.
type Pruner struct {
	Root         string
	RetentionFor RetentionFunc
	Now          func() time.Time
}

// NewPruner builds a Pruner. retention is applied to every source unless
// later replaced via the Pruner.RetentionFor field.
func NewPruner(root string, retention time.Duration) *Pruner {
	return &Pruner{
		Root:         root,
		RetentionFor: func(string) time.Duration { return retention },
		Now:          time.Now,
	}
}

// Prune walks the root and deletes every JSONL file dated before the
// retention cutoff for its source. Returns aggregate stats.
func (p *Pruner) Prune() (PruneResult, error) {
	if err := os.MkdirAll(p.Root, 0o755); err != nil {
		return PruneResult{}, fmt.Errorf("create logs root: %w", err)
	}
	now := p.Now
	if now == nil {
		now = time.Now
	}

	var result PruneResult
	entries, err := os.ReadDir(p.Root)
	if err != nil {
		return PruneResult{}, fmt.Errorf("read logs root: %w", err)
	}
	for _, src := range entries {
		if !src.IsDir() {
			continue
		}
		retention := p.RetentionFor(src.Name())
		if retention <= 0 {
			continue
		}
		cutoff := now().UTC().Add(-retention)
		dir := filepath.Join(p.Root, src.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return result, fmt.Errorf("read source %s: %w", src.Name(), err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			day, err := time.Parse("2006-01-02", strings.TrimSuffix(f.Name(), ".jsonl"))
			if err != nil {
				continue
			}
			if !day.Before(cutoff) {
				continue
			}
			path := filepath.Join(dir, f.Name())
			info, statErr := f.Info()
			if statErr == nil {
				result.FreedBytes += info.Size()
			}
			if err := os.Remove(path); err != nil {
				return result, fmt.Errorf("remove %s: %w", path, err)
			}
			result.DeletedFiles++
		}
	}
	return result, nil
}
