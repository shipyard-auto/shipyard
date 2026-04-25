package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store is a thread-safe writer of JSONL log lines into
// <root>/<source>/YYYY-MM-DD.jsonl files.
//
// One os.File is kept open per (source, day) pair under a mutex; rotation
// happens on the first Append after the calendar day changes (computed
// from the timestamp the caller supplies, not time.Now). Each Append is
// serialized so a single line never gets interleaved with another goroutine.
type Store struct {
	root string

	mu   sync.Mutex
	open map[string]*openFile
}

type openFile struct {
	f   *os.File
	day string
}

// NewStore creates a Store rooted at root. Directories are created lazily on
// first write so a brand new install does not require any pre-step.
func NewStore(root string) *Store {
	return &Store{root: root, open: make(map[string]*openFile)}
}

// Root returns the directory under which per-source subdirectories live.
func (s *Store) Root() string { return s.root }

// SourceDir returns the directory used for source's daily files.
func (s *Store) SourceDir(source string) string {
	return filepath.Join(s.root, source)
}

// DailyPath returns the path of the JSONL file for source on at's UTC date.
func (s *Store) DailyPath(source string, at time.Time) string {
	return filepath.Join(s.SourceDir(source), at.UTC().Format("2006-01-02")+".jsonl")
}

// Append writes line followed by a newline to the JSONL file owned by
// (source, at). The call performs a single os.File.Write under a per-Store
// mutex, so callers can rely on line-level atomicity.
//
// Errors from filesystem operations are returned. Callers that must not
// block (e.g. HTTP hot path) typically discard the error.
func (s *Store) Append(source string, at time.Time, line []byte) error {
	day := at.UTC().Format("2006-01-02")

	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.open[source]
	if !ok || cur.day != day {
		if cur != nil && cur.f != nil {
			_ = cur.f.Close()
		}
		f, err := s.openDay(source, day)
		if err != nil {
			return err
		}
		cur = &openFile{f: f, day: day}
		s.open[source] = cur
	}

	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	if _, err := cur.f.Write(buf); err != nil {
		return fmt.Errorf("append log line: %w", err)
	}
	return nil
}

// Close releases all open file descriptors. Subsequent Append calls reopen
// files transparently.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var first error
	for src, cur := range s.open {
		if cur != nil && cur.f != nil {
			if err := cur.f.Close(); err != nil && first == nil {
				first = err
			}
		}
		delete(s.open, src)
	}
	return first
}

func (s *Store) openDay(source, day string) (*os.File, error) {
	dir := s.SourceDir(source)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, day+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", path, err)
	}
	return f, nil
}
