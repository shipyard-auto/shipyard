package fairway

import (
	"errors"
	"fmt"
	"log/slog"
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
	return fmt.Sprintf("fairway já está rodando (PID %d)", e.PID)
}

// PIDFile represents an acquired PID lock file.
type PIDFile struct {
	path string
	pid  int
}

// ProcessChecker reports whether a process with the given PID is alive.
type ProcessChecker func(pid int) bool

// PIDFileOptions configures a PID file acquisition attempt.
// All fields have sensible defaults and may be left zero.
type PIDFileOptions struct {
	// Path is the absolute path of the PID file (required).
	Path string

	// Getpid returns the current process ID. Defaults to os.Getpid.
	Getpid func() int

	// IsAlive reports whether pid is a running process.
	// Defaults to defaultIsAlive (syscall.Kill-based).
	IsAlive ProcessChecker

	// beforeRetry is called just before the single retry attempt.
	// Unexported: only settable from within the package (internal tests).
	beforeRetry func()

	// afterExistFail is called after os.OpenFile returns ErrExist but before
	// os.ReadFile. Allows internal tests to simulate the TOCTOU race where the
	// file is deleted between the two syscalls.
	afterExistFail func()
}

// Acquire tries to create the PID file exclusively.
//
//   - File absent → create with mode 0600, write own PID, return PIDFile.
//   - File present, PID alive → return ErrAlreadyRunning.
//   - File present, PID stale or malformed → remove and retry once.
func Acquire(opts PIDFileOptions) (*PIDFile, error) {
	// Fill defaults.
	if opts.Getpid == nil {
		opts.Getpid = os.Getpid
	}
	if opts.IsAlive == nil {
		opts.IsAlive = defaultIsAlive
	}

	dir := filepath.Dir(opts.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create pidfile dir %s: %w", dir, err)
	}

	return acquire(opts, true)
}

// acquire is the internal recursive implementation; retried is false on the
// first call and true when retrying after a stale/malformed file was removed.
func acquire(opts PIDFileOptions, mayRetry bool) (*PIDFile, error) {
	pid := opts.Getpid()

	f, err := os.OpenFile(opts.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		// Successfully created — write PID and close.
		_, werr := fmt.Fprintf(f, "%d\n", pid)
		cerr := f.Close()
		if werr != nil {
			_ = os.Remove(opts.Path)
			return nil, fmt.Errorf("write pidfile: %w", werr)
		}
		if cerr != nil {
			_ = os.Remove(opts.Path)
			return nil, fmt.Errorf("close pidfile: %w", cerr)
		}
		return &PIDFile{path: opts.Path, pid: pid}, nil
	}

	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("open pidfile: %w", err)
	}

	// File already exists — read it.
	if opts.afterExistFail != nil {
		opts.afterExistFail()
	}
	data, readErr := os.ReadFile(opts.Path)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) && mayRetry {
			// Removed between our open and read — retry.
			return acquire(opts, false)
		}
		return nil, fmt.Errorf("read pidfile: %w", readErr)
	}

	existing, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil {
		// Malformed content — remove and retry once.
		slog.Warn("pidfile contains non-numeric PID; removing", "path", opts.Path)
		_ = os.Remove(opts.Path)
		if mayRetry {
			if opts.beforeRetry != nil {
				opts.beforeRetry()
			}
			return acquire(opts, false)
		}
		return nil, fmt.Errorf("pidfile malformed after retry")
	}

	if opts.IsAlive(existing) {
		return nil, ErrAlreadyRunning{PID: existing}
	}

	// Stale PID — process no longer running.
	slog.Warn("removing stale pidfile", "path", opts.Path, "stale_pid", existing)
	_ = os.Remove(opts.Path)
	if mayRetry {
		if opts.beforeRetry != nil {
			opts.beforeRetry()
		}
		return acquire(opts, false)
	}
	return nil, fmt.Errorf("could not acquire pidfile after removing stale entry")
}

// Release removes the PID file if it still contains our own PID.
// Idempotent: safe to call multiple times or when the file is absent.
func (p *PIDFile) Release() error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // already gone
		}
		return fmt.Errorf("read pidfile on release: %w", err)
	}

	stored, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil || stored != p.pid {
		// File was taken over by another process — do not remove it.
		slog.Warn("pidfile PID mismatch on release; skipping removal",
			"path", p.path, "stored", stored, "ours", p.pid)
		return nil
	}

	if err := os.Remove(p.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pidfile: %w", err)
	}
	return nil
}

// defaultIsAlive sends signal 0 to pid. Returns true if the process exists
// (errno == nil or EPERM), false if it does not (ESRCH).
// Works on Linux and macOS.
func defaultIsAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		return true // exists but owned by another user
	}
	return false // ESRCH or other — not running
}
