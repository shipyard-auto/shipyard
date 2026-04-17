package fairway_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	fairway "github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// alwaysAlive is an IsAlive stub that always returns true.
func alwaysAlive(_ int) bool { return true }

// neverAlive is an IsAlive stub that always returns false.
func neverAlive(_ int) bool { return false }

// pidPath returns a PID file path inside a fresh temp dir.
func pidPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "fairway.pid")
}

// ── Acquire tests ─────────────────────────────────────────────────────────────

func TestAcquire_firstTime_creates(t *testing.T) {
	t.Parallel()

	path := pidPath(t)
	pf, err := fairway.Acquire(fairway.PIDFileOptions{Path: path, IsAlive: neverAlive})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer pf.Release()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("PID file not created: %v", err)
	}
}

func TestAcquire_secondTime_whileAlive_returnsErrAlreadyRunning(t *testing.T) {
	t.Parallel()

	myPID := 42
	path := pidPath(t)

	// First acquire with a fake getpid returning 42.
	pf, err := fairway.Acquire(fairway.PIDFileOptions{
		Path:    path,
		Getpid:  func() int { return myPID },
		IsAlive: alwaysAlive,
	})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer pf.Release()

	// Second acquire — should see PID 42 alive and refuse.
	_, err = fairway.Acquire(fairway.PIDFileOptions{
		Path:    path,
		Getpid:  func() int { return 99 },
		IsAlive: alwaysAlive,
	})
	if err == nil {
		t.Fatal("expected ErrAlreadyRunning, got nil")
	}

	var alreadyRunning fairway.ErrAlreadyRunning
	if !errors.As(err, &alreadyRunning) {
		t.Fatalf("err type = %T; want ErrAlreadyRunning", err)
	}
	if alreadyRunning.PID != myPID {
		t.Errorf("PID = %d; want %d", alreadyRunning.PID, myPID)
	}
}

func TestAcquire_stalePID_removesAndSucceeds(t *testing.T) {
	t.Parallel()

	path := pidPath(t)

	// Write a "stale" PID file manually.
	os.WriteFile(path, []byte("99999\n"), 0600)

	// Acquire with IsAlive=false (stale).
	pf, err := fairway.Acquire(fairway.PIDFileOptions{
		Path:    path,
		IsAlive: neverAlive,
	})
	if err != nil {
		t.Fatalf("Acquire after stale: %v", err)
	}
	defer pf.Release()
}

func TestAcquire_malformedPIDFile_recoverable(t *testing.T) {
	t.Parallel()

	path := pidPath(t)
	os.WriteFile(path, []byte("not-a-pid\n"), 0600)

	pf, err := fairway.Acquire(fairway.PIDFileOptions{
		Path:    path,
		IsAlive: neverAlive,
	})
	if err != nil {
		t.Fatalf("Acquire after malformed: %v", err)
	}
	defer pf.Release()
}

func TestAcquire_creationFailsForReadOnlyDir_returnsError(t *testing.T) {
	t.Parallel()

	// Create a dir and make it read-only.
	dir := t.TempDir()
	roDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roDir, 0500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Ensure we can restore permissions for cleanup.
	t.Cleanup(func() { os.Chmod(roDir, 0700) })

	path := filepath.Join(roDir, "sub", "fairway.pid")
	_, err := fairway.Acquire(fairway.PIDFileOptions{Path: path, IsAlive: neverAlive})
	if err == nil {
		t.Fatal("expected error for read-only dir, got nil")
	}
}

func TestAcquire_createsDirWithMode0700(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	path := filepath.Join(base, "nested", "dir", "fairway.pid")

	pf, err := fairway.Acquire(fairway.PIDFileOptions{Path: path, IsAlive: neverAlive})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer pf.Release()

	info, err := os.Stat(filepath.Join(base, "nested"))
	if err != nil {
		t.Fatalf("stat nested dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("nested dir perm = %04o; want 0700", perm)
	}
}

func TestAcquire_fileMode0600(t *testing.T) {
	t.Parallel()

	path := pidPath(t)
	pf, err := fairway.Acquire(fairway.PIDFileOptions{Path: path, IsAlive: neverAlive})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer pf.Release()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat pidfile: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("pidfile perm = %04o; want 0600", perm)
	}
}

// ── Release tests ─────────────────────────────────────────────────────────────

func TestRelease_idempotent(t *testing.T) {
	t.Parallel()

	path := pidPath(t)
	pf, err := fairway.Acquire(fairway.PIDFileOptions{Path: path, IsAlive: neverAlive})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	if err := pf.Release(); err != nil {
		t.Errorf("first Release: %v", err)
	}
	if err := pf.Release(); err != nil {
		t.Errorf("second Release: %v", err)
	}

	// File must not exist.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected pidfile to be absent; stat err = %v", err)
	}
}

func TestRelease_onlyRemovesOwnPID(t *testing.T) {
	t.Parallel()

	path := pidPath(t)

	myPID := 1001
	pf, err := fairway.Acquire(fairway.PIDFileOptions{
		Path:    path,
		Getpid:  func() int { return myPID },
		IsAlive: neverAlive,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Overwrite the file with a different PID to simulate a takeover.
	otherPID := 2002
	os.WriteFile(path, []byte(strconv.Itoa(otherPID)+"\n"), 0600)

	// Release should NOT remove the file (PID mismatch).
	if err := pf.Release(); err != nil {
		t.Errorf("Release returned error: %v", err)
	}

	// File must still exist.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("pidfile should still exist after PID mismatch Release: %v", err)
	}
}

// TestRelease_malformedContent_doesNotRemove verifies that Release skips removal
// when the file content is no longer a valid PID (e.g. corrupted).
func TestRelease_malformedContent_doesNotRemove(t *testing.T) {
	t.Parallel()

	path := pidPath(t)
	pf, err := fairway.Acquire(fairway.PIDFileOptions{Path: path, IsAlive: neverAlive})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Overwrite with garbage so parseErr != nil in Release.
	os.WriteFile(path, []byte("garbage\n"), 0600)

	if err := pf.Release(); err != nil {
		t.Errorf("Release returned error: %v", err)
	}
	// File must still exist (we skipped removal due to mismatch).
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file to remain after malformed-content Release: %v", err)
	}
}

// ── defaultIsAlive (via Acquire with no custom IsAlive) ───────────────────────

// TestDefaultIsAlive_currentProcessIsAlive writes the current test process PID
// to the file and expects Acquire (without IsAlive override) to detect it alive.
func TestDefaultIsAlive_currentProcessIsAlive(t *testing.T) {
	t.Parallel()

	path := pidPath(t)
	myPID := os.Getpid()
	os.WriteFile(path, []byte(strconv.Itoa(myPID)+"\n"), 0600)

	_, err := fairway.Acquire(fairway.PIDFileOptions{Path: path}) // uses defaultIsAlive
	if err == nil {
		t.Fatal("expected ErrAlreadyRunning for current (live) PID, got nil")
	}
	var alr fairway.ErrAlreadyRunning
	if !errors.As(err, &alr) {
		t.Fatalf("err type = %T; want ErrAlreadyRunning", err)
	}
	if alr.PID != myPID {
		t.Errorf("PID = %d; want %d", alr.PID, myPID)
	}
}

// TestDefaultIsAlive_deadProcessIsStale starts a short-lived subprocess, waits
// for it to exit, then verifies Acquire treats its PID as stale.
func TestDefaultIsAlive_deadProcessIsStale(t *testing.T) {
	t.Parallel()

	// Use the test binary itself with a run filter that matches nothing,
	// so it starts and exits immediately.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Skipf("spawn short process: %v", err)
	}
	deadPID := cmd.ProcessState.Pid()

	path := pidPath(t)
	os.WriteFile(path, []byte(strconv.Itoa(deadPID)+"\n"), 0600)

	// Acquire without IsAlive override — defaultIsAlive should detect PID dead.
	pf, err := fairway.Acquire(fairway.PIDFileOptions{Path: path})
	if err != nil {
		t.Fatalf("Acquire with dead PID: %v", err)
	}
	defer pf.Release()
}

// TestDefaultIsAlive_rootProcessIsAlive uses PID 1 (init/launchd), which
// exists and is owned by root. A non-root process gets EPERM from kill(1,0),
// which defaultIsAlive correctly treats as "alive".
func TestDefaultIsAlive_rootProcessIsAlive(t *testing.T) {
	t.Parallel()

	path := pidPath(t)
	os.WriteFile(path, []byte("1\n"), 0600)

	_, err := fairway.Acquire(fairway.PIDFileOptions{Path: path}) // uses defaultIsAlive
	if err == nil {
		t.Fatal("expected ErrAlreadyRunning for PID 1, got nil")
	}
	var alr fairway.ErrAlreadyRunning
	if !errors.As(err, &alr) {
		t.Fatalf("err type = %T; want ErrAlreadyRunning", err)
	}
}

// ── ErrAlreadyRunning ─────────────────────────────────────────────────────────

func TestErrAlreadyRunning_errorMessage(t *testing.T) {
	err := fairway.ErrAlreadyRunning{PID: 1234}
	want := "fairway já está rodando (PID 1234)"
	if err.Error() != want {
		t.Errorf("Error() = %q; want %q", err.Error(), want)
	}
}
