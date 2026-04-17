package fairway

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var ErrAlreadyRunning = errors.New("fairway daemon already running")

// AlreadyRunningError reports that another Fairway daemon instance holds the pidfile lock.
type AlreadyRunningError struct {
	Path string
	PID  int
}

func (e *AlreadyRunningError) Error() string {
	if e == nil {
		return ErrAlreadyRunning.Error()
	}
	if e.PID > 0 {
		return fmt.Sprintf("%s: pid %d", ErrAlreadyRunning.Error(), e.PID)
	}
	if e.Path != "" {
		return fmt.Sprintf("%s: %s", ErrAlreadyRunning.Error(), e.Path)
	}
	return ErrAlreadyRunning.Error()
}

// Unwrap allows errors.Is(err, ErrAlreadyRunning).
func (e *AlreadyRunningError) Unwrap() error {
	return ErrAlreadyRunning
}

// IsAlreadyRunning unwraps ErrAlreadyRunning with typed metadata when available.
func IsAlreadyRunning(err error) (*AlreadyRunningError, bool) {
	var runningErr *AlreadyRunningError
	if errors.As(err, &runningErr) {
		return runningErr, true
	}
	return nil, false
}

// PIDFile coordinates single-instance Fairway execution through a locked pidfile.
type PIDFile struct {
	path string

	mu   sync.Mutex
	file *os.File
}

// NewPIDFile returns a pidfile lock for the provided path.
func NewPIDFile(path string) *PIDFile {
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}
	return &PIDFile{path: path}
}

// DefaultPIDFilePath returns the default Fairway pidfile location.
func DefaultPIDFilePath() (string, error) {
	return defaultRuntimePath("fairway.pid")
}

// Path returns the resolved pidfile path.
func (p *PIDFile) Path() string {
	return p.path
}

// Acquire locks the pidfile, writes the current process ID, and keeps the file open until Release.
func (p *PIDFile) Acquire() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.file != nil {
		return nil
	}

	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create pid dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod pid dir %s: %w", dir, err)
	}

	file, err := os.OpenFile(p.path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open pidfile %s: %w", p.path, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		runningErr := &AlreadyRunningError{
			Path: p.path,
			PID:  readPID(file),
		}
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return runningErr
		}
		return fmt.Errorf("lock pidfile %s: %w", p.path, err)
	}

	if err := file.Chmod(0o600); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return fmt.Errorf("chmod pidfile %s: %w", p.path, err)
	}

	if err := file.Truncate(0); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return fmt.Errorf("truncate pidfile %s: %w", p.path, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return fmt.Errorf("seek pidfile %s: %w", p.path, err)
	}

	if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return fmt.Errorf("write pidfile %s: %w", p.path, err)
	}
	if err := file.Sync(); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return fmt.Errorf("sync pidfile %s: %w", p.path, err)
	}

	p.file = file
	return nil
}

// Release unlocks and removes the pidfile.
func (p *PIDFile) Release() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.file == nil {
		return nil
	}

	file := p.file
	p.file = nil

	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	removeErr := os.Remove(p.path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}

	if unlockErr != nil {
		return fmt.Errorf("unlock pidfile %s: %w", p.path, unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close pidfile %s: %w", p.path, closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove pidfile %s: %w", p.path, removeErr)
	}
	return nil
}

func defaultRuntimePath(name string) (string, error) {
	if shipyardHome := os.Getenv("SHIPYARD_HOME"); shipyardHome != "" {
		return filepath.Join(shipyardHome, "run", name), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home dir: %w", err)
	}
	return filepath.Join(homeDir, ".shipyard", "run", name), nil
}

func readPID(file *os.File) int {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
