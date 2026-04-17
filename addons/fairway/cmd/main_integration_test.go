//go:build integration

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

func buildFairwayBinary(t *testing.T) string {
	t.Helper()

	buildOnce.Do(func() {
		tmpDir := os.TempDir()
		binPath = filepath.Join(tmpDir, "shipyard-fairway-integration-bin")
		cmd := exec.Command("go", "build", "-o", binPath, "./addons/fairway/cmd")
		cmd.Dir = projectRoot(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = &exec.ExitError{}
			buildErr = err
			buildErr = &buildFailure{err: err, output: string(out)}
		}
	})

	if buildErr != nil {
		t.Fatalf("go build failed: %v", buildErr)
	}
	return binPath
}

type buildFailure struct {
	err    error
	output string
}

func (e *buildFailure) Error() string {
	return e.err.Error() + ": " + e.output
}

func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	return root
}

func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer lis.Close()
	return lis.Addr().(*net.TCPAddr).Port
}

func writeConfig(t *testing.T, shipyardHome string, port int) string {
	t.Helper()
	configPath := filepath.Join(shipyardHome, "fairway", "routes.json")
	repo := fairway.NewFileRepositoryAt(configPath)
	cfg := fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          port,
		Bind:          fairway.DefaultBind,
		Routes:        []fairway.Route{},
	}
	if err := repo.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return configPath
}

func waitForPath(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("path %s did not appear within %s", path, timeout)
}

func waitForHTTP(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := net.DialTimeout("tcp", strings.TrimPrefix(url, "http://"), 100*time.Millisecond)
		if err == nil {
			_ = resp.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("listener %s did not become ready within %s", url, timeout)
}

func TestVersion_flagPrintsVersion_exit0(t *testing.T) {
	bin := buildFairwayBinary(t)
	cmd := exec.Command(bin, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run --version: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "shipyard-fairway") {
		t.Fatalf("output = %q; want version string", string(out))
	}
}

func TestHelp_flagExit0(t *testing.T) {
	bin := buildFairwayBinary(t)
	cmd := exec.Command(bin, "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run --help: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Usage: shipyard-fairway") {
		t.Fatalf("output = %q; want usage", string(out))
	}
}

func TestAlreadyRunning_exit10(t *testing.T) {
	bin := buildFairwayBinary(t)
	home := t.TempDir()
	runDir := filepath.Join(home, "run")
	pid, err := fairway.Acquire(fairway.PIDFileOptions{Path: filepath.Join(runDir, "fairway.pid")})
	if err != nil {
		t.Fatalf("acquire pid: %v", err)
	}
	defer pid.Release() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(), "SHIPYARD_HOME="+home)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected exit 10, got success: %s", out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("err = %T %v", err, err)
	}
	if exitErr.ExitCode() != exitAlreadyRunning {
		t.Fatalf("exit code = %d; want %d\n%s", exitErr.ExitCode(), exitAlreadyRunning, out)
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("second instance took %s; want < 100ms", elapsed)
	}
}

func TestSIGINT_exitCleanly(t *testing.T) {
	bin := buildFairwayBinary(t)
	home := t.TempDir()
	port := freePort(t)
	writeConfig(t, home, port)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "SHIPYARD_HOME="+home)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	waitForPath(t, filepath.Join(home, "run", "fairway.sock"), 5*time.Second)
	waitForHTTP(t, net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)), 5*time.Second)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal interrupt: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait after SIGINT: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("binary did not exit within 5s after SIGINT")
	}

	if _, err := os.Stat(filepath.Join(home, "run", "fairway.sock")); !os.IsNotExist(err) {
		t.Fatalf("socket file should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "run", "fairway.pid")); !os.IsNotExist(err) {
		t.Fatalf("pid file should be removed, stat err = %v", err)
	}
}

func TestInvalidConfig_exit20(t *testing.T) {
	bin := buildFairwayBinary(t)
	home := t.TempDir()
	configPath := filepath.Join(home, "fairway", "routes.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":"2","port":9876,"bind":"127.0.0.1","routes":[]}`), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "--config", configPath)
	cmd.Env = append(os.Environ(), "SHIPYARD_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected exit 20, got success: %s", out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("err = %T %v", err, err)
	}
	if exitErr.ExitCode() != exitInvalidConfig {
		t.Fatalf("exit code = %d; want %d\n%s", exitErr.ExitCode(), exitInvalidConfig, out)
	}
}

func TestMissingConfigPath_exit0WhenParentExists(t *testing.T) {
	bin := buildFairwayBinary(t)
	home, err := os.MkdirTemp("", "fw011-missing-")
	if err != nil {
		t.Fatalf("mkdir temp home: %v", err)
	}
	defer os.RemoveAll(home) //nolint:errcheck

	missingConfigPath := filepath.Join(home, "fairway", "missing-routes.json")
	if err := os.MkdirAll(filepath.Dir(missingConfigPath), 0700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	cmd := exec.Command(bin, "--config", missingConfigPath)
	cmd.Env = append(os.Environ(), "SHIPYARD_HOME="+home)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	waitForPath(t, filepath.Join(home, "run", "fairway.pid"), 5*time.Second)
	waitForPath(t, filepath.Join(home, "run", "fairway.sock"), 5*time.Second)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal interrupt: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait after SIGINT: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("binary did not exit within 5s after SIGINT")
	}
}

var _ = syscall.SIGTERM
