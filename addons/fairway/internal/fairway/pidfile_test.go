package fairway

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDefaultPIDFilePath(t *testing.T) {
	t.Run("UsesShipyardHomeWhenPresent", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("SHIPYARD_HOME", root)

		path, err := DefaultPIDFilePath()
		if err != nil {
			t.Fatalf("DefaultPIDFilePath() error = %v", err)
		}

		want := filepath.Join(root, "run", "fairway.pid")
		if path != want {
			t.Fatalf("DefaultPIDFilePath() = %q, want %q", path, want)
		}
	})

	t.Run("FallsBackToHomeDir", func(t *testing.T) {
		t.Setenv("SHIPYARD_HOME", "")
		root := t.TempDir()
		t.Setenv("HOME", root)

		path, err := DefaultPIDFilePath()
		if err != nil {
			t.Fatalf("DefaultPIDFilePath() error = %v", err)
		}

		want := filepath.Join(root, ".shipyard", "run", "fairway.pid")
		if path != want {
			t.Fatalf("DefaultPIDFilePath() = %q, want %q", path, want)
		}
	})
}

func TestPIDFileAcquireWritesPIDAndReleaseRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "fairway.pid")
	pidfile := NewPIDFile(path)

	if err := pidfile.Acquire(); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer func() {
		if err := pidfile.Release(); err != nil {
			t.Fatalf("Release() cleanup error = %v", err)
		}
	}()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", path, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("pidfile mode = %o, want 600", mode)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", filepath.Dir(path), err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("pid dir mode = %o, want 700", mode)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	if got, want := strings.TrimSpace(string(data)), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("pidfile content = %q, want %q", got, want)
	}

	if err := pidfile.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) error = %v, want not exist", path, err)
	}
}

func TestPIDFileAcquireReturnsAlreadyRunningError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fairway.pid")
	cmd, stdin, helperPID := startPIDFileHelper(t, path)
	defer stopPIDFileHelper(t, cmd, stdin)

	pidfile := NewPIDFile(path)
	err := pidfile.Acquire()
	if err == nil {
		_ = pidfile.Release()
		t.Fatal("Acquire() error = nil, want already running")
	}

	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("errors.Is(err, ErrAlreadyRunning) = false, err = %v", err)
	}

	runningErr, ok := IsAlreadyRunning(err)
	if !ok {
		t.Fatalf("IsAlreadyRunning(%v) = false, want true", err)
	}
	if runningErr.PID != helperPID {
		t.Fatalf("AlreadyRunningError.PID = %d, want %d", runningErr.PID, helperPID)
	}
	if runningErr.Path != path {
		t.Fatalf("AlreadyRunningError.Path = %q, want %q", runningErr.Path, path)
	}
}

func TestPIDFileAcquireReusesUnlockedExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fairway.pid")
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}

	pidfile := NewPIDFile(path)
	if err := pidfile.Acquire(); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer func() {
		if err := pidfile.Release(); err != nil {
			t.Fatalf("Release() cleanup error = %v", err)
		}
	}()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	if got, want := strings.TrimSpace(string(data)), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("pidfile content = %q, want %q", got, want)
	}
}

func TestPIDFileAcquireIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fairway.pid")
	pidfile := NewPIDFile(path)

	if err := pidfile.Acquire(); err != nil {
		t.Fatalf("Acquire() first error = %v", err)
	}
	defer func() {
		if err := pidfile.Release(); err != nil {
			t.Fatalf("Release() cleanup error = %v", err)
		}
	}()

	if err := pidfile.Acquire(); err != nil {
		t.Fatalf("Acquire() second error = %v", err)
	}
}

func TestPIDFileReleaseWithoutAcquire(t *testing.T) {
	pidfile := NewPIDFile(filepath.Join(t.TempDir(), "fairway.pid"))
	if err := pidfile.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
}

func TestHelperPIDFileProcess(t *testing.T) {
	if os.Getenv("GO_WANT_FAIRWAY_PIDFILE_HELPER") != "1" {
		return
	}

	path := os.Getenv("FAIRWAY_PIDFILE_PATH")
	if path == "" {
		fmt.Fprintln(os.Stderr, "missing FAIRWAY_PIDFILE_PATH")
		os.Exit(2)
	}

	pidfile := NewPIDFile(path)
	if err := pidfile.Acquire(); err != nil {
		fmt.Fprintf(os.Stderr, "Acquire() error: %v\n", err)
		os.Exit(2)
	}
	defer func() {
		_ = pidfile.Release()
	}()

	fmt.Fprintln(os.Stdout, os.Getpid())
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func startPIDFileHelper(t *testing.T, path string) (*exec.Cmd, io.WriteCloser, int) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperPIDFileProcess")
	cmd.Env = append(os.Environ(),
		"GO_WANT_FAIRWAY_PIDFILE_HELPER=1",
		"FAIRWAY_PIDFILE_PATH="+path,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe() error = %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("ReadString() error = %v", err)
	}

	helperPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("Atoi(%q) error = %v", line, err)
	}

	return cmd, stdin, helperPID
}

func stopPIDFileHelper(t *testing.T, cmd *exec.Cmd, stdin io.Closer) {
	t.Helper()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil {
		return
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}
