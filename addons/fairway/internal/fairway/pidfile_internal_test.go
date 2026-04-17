// pidfile_internal_test.go — package-internal tests for edge paths in
// acquire() that require access to unexported PIDFileOptions fields.
package fairway

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAcquire_retryStillStale_returnsError verifies the terminal error path
// when the stale file is recreated (by a concurrent writer) between the
// removal and the retry, and the retry again finds a stale PID.
func TestAcquire_retryStillStale_returnsError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "fairway.pid")

	// First pass: file has stale PID 99999.
	os.WriteFile(path, []byte("99999\n"), 0600)

	// beforeRetry: recreate with another stale PID so the retry also fails.
	opts := PIDFileOptions{
		Path:    path,
		IsAlive: func(int) bool { return false }, // all PIDs appear stale
		beforeRetry: func() {
			os.WriteFile(path, []byte("99998\n"), 0600)
		},
	}

	_, err := Acquire(opts)
	if err == nil {
		t.Fatal("expected error when retry also finds a stale PID, got nil")
	}
}

// TestAcquire_retryStillMalformed_returnsError verifies the terminal error path
// when the malformed file is recreated between the removal and the retry.
func TestAcquire_retryStillMalformed_returnsError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "fairway.pid")

	// First pass: malformed content.
	os.WriteFile(path, []byte("bad\n"), 0600)

	opts := PIDFileOptions{
		Path:    path,
		IsAlive: func(int) bool { return false },
		beforeRetry: func() {
			// Recreate with malformed content — retry will fail too.
			os.WriteFile(path, []byte("still-bad\n"), 0600)
		},
	}

	_, err := Acquire(opts)
	if err == nil {
		t.Fatal("expected error when retry also finds malformed PID, got nil")
	}
}

// TestAcquire_openErrNotExist_returnsError verifies the acquire path where
// os.OpenFile fails with an error that is NOT ErrExist.
// This happens when the pid path is itself a directory (EISDIR ≠ EEXIST).
func TestAcquire_openErrNotExist_returnsError(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	// The pid path IS a directory — OpenFile will get EISDIR, not EEXIST.
	path := filepath.Join(base, "fairway.pid")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(path) })

	_, err := Acquire(PIDFileOptions{Path: path, IsAlive: func(int) bool { return false }})
	if err == nil {
		t.Fatal("expected error when pid path is a directory")
	}
}

// TestAcquire_toctouRace_retries verifies the TOCTOU path where the file is
// deleted between os.OpenFile returning ErrExist and os.ReadFile being called.
// The retry should then succeed (file is gone).
func TestAcquire_toctouRace_retries(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "fairway.pid")
	// Pre-create the file so OpenFile returns ErrExist on first call.
	os.WriteFile(path, []byte("99999\n"), 0600)

	opts := PIDFileOptions{
		Path:    path,
		IsAlive: func(int) bool { return false },
		// Delete the file after OpenFile fails — simulates TOCTOU race.
		afterExistFail: func() { os.Remove(path) },
	}

	pf, err := Acquire(opts)
	if err != nil {
		t.Fatalf("expected success after TOCTOU retry, got: %v", err)
	}
	defer pf.Release()
}

// TestRelease_readError_returnsError verifies the Release error path when the
// file exists but can't be read (permission denied).
func TestRelease_readError_returnsError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "fairway.pid")

	// Acquire normally.
	pf, err := Acquire(PIDFileOptions{Path: path, IsAlive: func(int) bool { return false }})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Remove the file and replace with a directory of the same name so
	// ReadFile returns an error that is NOT ErrNotExist.
	os.Remove(path)
	os.Mkdir(path, 0700)
	t.Cleanup(func() { os.Remove(path) })

	err = pf.Release()
	if err == nil {
		t.Error("expected Release to return an error when file is unreadable")
	}

	// Verify it's not an ErrNotExist-masked error.
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected non-ErrNotExist error; got ErrNotExist")
	}
}
