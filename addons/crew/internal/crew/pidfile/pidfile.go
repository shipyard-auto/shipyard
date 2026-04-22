// Package pidfile implements a single-instance lock for the shipyard-crew
// daemon. It mirrors addons/fairway/internal/fairway/pidfile.go: O_CREATE|
// O_EXCL acquisition, stale-PID detection via signal 0, atomic Release that
// only removes the file when it still contains our own PID.
//
// Debt: the fairway and crew implementations are duplicated. v2 should
// promote a shared pkg/pidfile.
package pidfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrAlreadyRunning is returned by Acquire when another live process already
// holds the PID file.
type ErrAlreadyRunning struct {
	PID int
}

func (e ErrAlreadyRunning) Error() string {
	return fmt.Sprintf("crew daemon already running (PID %d)", e.PID)
}

// PIDFile represents an acquired PID lock. Release must be called when the
// daemon exits.
type PIDFile struct {
	path string
	pid  int
}

// Path returns the lock file path.
func (p *PIDFile) Path() string { return p.path }

// PID returns the PID written to the file.
func (p *PIDFile) PID() int { return p.pid }

// ProcessChecker reports whether a process with the given PID is alive.
type ProcessChecker func(pid int) bool

// Options configures Acquire. Only Path is required.
type Options struct {
	Path    string
	Getpid  func() int
	IsAlive ProcessChecker
}

// Acquire is the convenience entry point: acquires the PID file at path with
// default Getpid (os.Getpid) and IsAlive (signal-0 probe).
func Acquire(path string) (*PIDFile, error) {
	return AcquireWith(Options{Path: path})
}

// AcquireWith creates the PID file exclusively at opts.Path. If the file
// already exists and the PID inside is alive, ErrAlreadyRunning is returned.
// Stale or malformed PIDs are removed and the acquisition is retried once.
func AcquireWith(opts Options) (*PIDFile, error) {
	if opts.Path == "" {
		return nil, errors.New("pidfile: path is required")
	}
	if opts.Getpid == nil {
		opts.Getpid = os.Getpid
	}
	if opts.IsAlive == nil {
		opts.IsAlive = defaultIsAlive
	}

	dir := filepath.Dir(opts.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("pidfile mkdir %s: %w", dir, err)
	}
	return acquire(opts, true)
}

func acquire(opts Options, mayRetry bool) (*PIDFile, error) {
	pid := opts.Getpid()

	f, err := os.OpenFile(opts.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, werr := fmt.Fprintf(f, "%d\n", pid); werr != nil {
			_ = f.Close()
			_ = os.Remove(opts.Path)
			return nil, fmt.Errorf("pidfile write: %w", werr)
		}
		if cerr := f.Close(); cerr != nil {
			_ = os.Remove(opts.Path)
			return nil, fmt.Errorf("pidfile close: %w", cerr)
		}
		return &PIDFile{path: opts.Path, pid: pid}, nil
	}

	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("pidfile open: %w", err)
	}

	data, readErr := os.ReadFile(opts.Path)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) && mayRetry {
			return acquire(opts, false)
		}
		return nil, fmt.Errorf("pidfile read: %w", readErr)
	}

	existing, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil {
		_ = os.Remove(opts.Path)
		if mayRetry {
			return acquire(opts, false)
		}
		return nil, fmt.Errorf("pidfile malformed after retry")
	}

	if opts.IsAlive(existing) {
		return nil, ErrAlreadyRunning{PID: existing}
	}

	_ = os.Remove(opts.Path)
	if mayRetry {
		return acquire(opts, false)
	}
	return nil, fmt.Errorf("pidfile could not be acquired after removing stale entry")
}

// Release removes the PID file if it still contains our own PID. Idempotent.
func (p *PIDFile) Release() error {
	if p == nil {
		return nil
	}
	data, err := os.ReadFile(p.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("pidfile read on release: %w", err)
	}
	stored, perr := strconv.Atoi(strings.TrimSpace(string(data)))
	if perr != nil || stored != p.pid {
		return nil
	}
	if err := os.Remove(p.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("pidfile remove: %w", err)
	}
	return nil
}

func defaultIsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	return false
}
