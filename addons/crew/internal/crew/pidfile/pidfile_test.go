package pidfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tmpPath(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, name)
}

func TestAcquireCreatesAndReleaseRemoves(t *testing.T) {
	path := tmpPath(t, "x.pid")
	pf, err := AcquireWith(Options{Path: path, Getpid: func() int { return 1234 }})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "1234" {
		t.Errorf("contents = %q, want 1234", got)
	}
	if err := pf.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file should be removed: %v", err)
	}
}

func TestAcquireFailsWhenLiveProcessHoldsFile(t *testing.T) {
	path := tmpPath(t, "x.pid")
	if err := os.WriteFile(path, []byte("4242\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := AcquireWith(Options{
		Path:    path,
		Getpid:  func() int { return 1 },
		IsAlive: func(pid int) bool { return pid == 4242 },
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ar ErrAlreadyRunning
	if !errors.As(err, &ar) {
		t.Fatalf("want ErrAlreadyRunning, got %T: %v", err, err)
	}
	if ar.PID != 4242 {
		t.Errorf("ErrAlreadyRunning.PID = %d, want 4242", ar.PID)
	}
}

func TestAcquireOverwritesStalePID(t *testing.T) {
	path := tmpPath(t, "x.pid")
	if err := os.WriteFile(path, []byte("99999\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pf, err := AcquireWith(Options{
		Path:    path,
		Getpid:  func() int { return 7 },
		IsAlive: func(pid int) bool { return false },
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if pf.pid != 7 {
		t.Errorf("pid = %d, want 7", pf.pid)
	}
	data, _ := os.ReadFile(path)
	if got := strings.TrimSpace(string(data)); got != "7" {
		t.Errorf("contents = %q, want 7", got)
	}
}

func TestAcquireOverwritesMalformedFile(t *testing.T) {
	path := tmpPath(t, "x.pid")
	if err := os.WriteFile(path, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pf, err := AcquireWith(Options{Path: path, Getpid: func() int { return 11 }})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if pf.pid != 11 {
		t.Errorf("pid = %d, want 11", pf.pid)
	}
}

func TestReleaseSkipsForeignPID(t *testing.T) {
	path := tmpPath(t, "x.pid")
	pf, err := AcquireWith(Options{Path: path, Getpid: func() int { return 100 }})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Simulate another process taking over.
	if err := os.WriteFile(path, []byte("9999\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := pf.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should still exist: %v", err)
	}
}

func TestReleaseIdempotent(t *testing.T) {
	path := tmpPath(t, "x.pid")
	pf, err := AcquireWith(Options{Path: path, Getpid: func() int { return 5 }})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := pf.Release(); err != nil {
		t.Fatalf("release1: %v", err)
	}
	if err := pf.Release(); err != nil {
		t.Fatalf("release2: %v", err)
	}
}

func TestAcquireRequiresPath(t *testing.T) {
	if _, err := AcquireWith(Options{}); err == nil {
		t.Fatalf("expected error for empty path")
	}
}

func TestAcquireDefault(t *testing.T) {
	path := tmpPath(t, "x.pid")
	pf, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if pf.pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pf.pid, os.Getpid())
	}
	_ = pf.Release()
}

func TestErrAlreadyRunningMessage(t *testing.T) {
	e := ErrAlreadyRunning{PID: 42}
	if got := e.Error(); !strings.Contains(got, "42") || !strings.Contains(got, "crew") {
		t.Errorf("message = %q", got)
	}
}

func TestPathAndPIDGetters(t *testing.T) {
	path := tmpPath(t, "x.pid")
	pf, err := AcquireWith(Options{Path: path, Getpid: func() int { return 77 }})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pf.Release()
	if pf.Path() != path {
		t.Errorf("Path = %q", pf.Path())
	}
	if pf.PID() != 77 {
		t.Errorf("PID = %d", pf.PID())
	}
}

func TestDefaultIsAlive(t *testing.T) {
	if !defaultIsAlive(os.Getpid()) {
		t.Errorf("current pid should be alive")
	}
	if defaultIsAlive(-1) {
		t.Errorf("pid -1 should not be alive")
	}
	if defaultIsAlive(0) {
		t.Errorf("pid 0 should not be alive")
	}
	// A very high pid is almost certainly not live — exercise the !EPERM
	// branch that returns false.
	if defaultIsAlive(99999999) {
		t.Errorf("pid 99999999 should not be alive")
	}
}

func TestAcquireOpenNonExistErrorPropagates(t *testing.T) {
	// Parent is a regular file, so MkdirAll fails → returns non-ErrExist.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := AcquireWith(Options{Path: filepath.Join(blocker, "sub", "x.pid")})
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestAcquireRetryExhaustedMalformed(t *testing.T) {
	// Drive the deepest branch: malformed file + mayRetry=false (by using the
	// unexported acquire directly).
	path := tmpPath(t, "x.pid")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := acquire(Options{Path: path, Getpid: func() int { return 5 }, IsAlive: func(int) bool { return false }}, false)
	// With mayRetry=false + malformed, the function removes the file but
	// returns the "malformed after retry" error.
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestReleaseNilIsNoop(t *testing.T) {
	var p *PIDFile
	if err := p.Release(); err != nil {
		t.Errorf("nil Release = %v", err)
	}
}

func TestSocketModePermissions(t *testing.T) {
	path := tmpPath(t, "x.pid")
	pf, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pf.Release()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}
