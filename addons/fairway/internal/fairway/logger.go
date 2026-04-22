package fairway

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// RequestEvent is a single structured log entry written to a JSONL log file.
type RequestEvent struct {
	Timestamp string    `json:"timestamp"`
	Source    string    `json:"source"`
	Level     string    `json:"level"`
	Event     string    `json:"event"`
	Message   string    `json:"message"`
	Data      EventData `json:"data"`
}

// EventData holds the per-request fields logged in each RequestEvent.
type EventData struct {
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

// RequestLogger writes RequestEvents to daily-rotating JSONL files under dir.
type RequestLogger struct {
	dir         string
	now         func() time.Time
	mu          sync.Mutex
	currentDay  string
	currentFile *os.File
}

// NewRequestLogger creates a RequestLogger that writes to dir.
// If now is nil, time.Now is used. The directory is created with mode 0700 if
// it does not already exist.
func NewRequestLogger(dir string, now func() time.Time) (*RequestLogger, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}
	if now == nil {
		now = time.Now
	}
	return &RequestLogger{
		dir: dir,
		now: now,
	}, nil
}

// Log writes event as a JSON line to the current log file, rotating to a new
// file if the calendar day has changed since the last write.
// Errors from os.OpenFile are returned; errors from json.Encode are returned.
// Callers that do not want to block the hot path should ignore the return value.
func (l *RequestLogger) Log(event RequestEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	day := l.now().UTC().Format("2006-01-02")
	if day != l.currentDay {
		// Day changed (or first write): close old file and open new one.
		if l.currentFile != nil {
			_ = l.currentFile.Close()
			l.currentFile = nil
		}

		path := fmt.Sprintf("%s/%s.jsonl", l.dir, day)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("open log file %s: %w", path, err)
		}
		l.currentFile = f
		l.currentDay = day
	}

	return json.NewEncoder(l.currentFile).Encode(event)
}

// Close closes the current log file if open. It is idempotent.
func (l *RequestLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.currentFile == nil {
		return nil
	}
	err := l.currentFile.Close()
	l.currentFile = nil
	return err
}
