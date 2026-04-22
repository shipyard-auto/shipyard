// Package crewctl provides core-side helpers that drive the lifecycle of
// `shipyard-crew` agents. The first responsibility (Task 23) is registering
// per-agent OS services: launchd user agents on macOS, systemd user units
// on Linux.
//
// Debt: internal/service/ already exists for the broader shipyard service
// CRUD, but it owns its own JSON store, uppercase IDs and shell wrappers,
// and does not expose a "Register(label, exec, args)" entry point. v2 must
// consolidate both packages behind a shared low-level registration API.
package crewctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Supported GOOS values. Other platforms cause Register/Unregister to fail
// with ErrUnsupportedPlatform.
const (
	PlatformDarwin = "darwin"
	PlatformLinux  = "linux"
)

// ErrUnsupportedPlatform is returned when the host OS has neither launchd
// nor user-systemd available.
var ErrUnsupportedPlatform = errors.New("unsupported platform")

// agentNameRe enforces the regex from Task 23. The agent name is interpolated
// into filesystem paths and command arguments, so injection-safety is hard
// requirement.
var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-_]*$`)

// CommandRunner abstracts external process execution so tests can assert on
// the launchctl/systemctl invocations without spawning real binaries.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// DefaultRunner returns a CommandRunner backed by os/exec.CommandContext. It
// is what the production callers use; tests should substitute a fake.
func DefaultRunner() CommandRunner { return execRunner{} }

// Manager is the entry point for registering/unregistering crew agents as
// OS-level services. All fields are required except Runner (defaults to
// DefaultRunner) and Platform (defaults to runtime.GOOS).
type Manager struct {
	// HomeDir is the user home; resolved once by NewManager so tests can
	// substitute it. Used to derive plist / unit / log paths.
	HomeDir string

	// Platform overrides runtime.GOOS for tests.
	Platform string

	// Runner executes launchctl / systemctl. Tests inject a fake.
	Runner CommandRunner

	// UID is forwarded into launchd's `gui/<uid>` domain. Defaults to
	// os.Getuid() at construction time. Tests may override.
	UID int
}

// NewManager builds a Manager rooted at the current user's home directory
// with the host OS as platform and DefaultRunner as runner.
func NewManager() (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	return &Manager{
		HomeDir:  home,
		Platform: runtime.GOOS,
		Runner:   DefaultRunner(),
		UID:      os.Getuid(),
	}, nil
}

// Paths bundles the filesystem locations Manager derives from an agent name.
// Exported so callers (CLI, tests) can introspect.
type Paths struct {
	UnitFile string // plist on macOS, .service on linux
	LogDir   string
	OutLog   string
	ErrLog   string
	Label    string // launchd Label or systemd unit name
}

// PathsFor returns the Paths for the given agent. It does not validate the
// agent name; call ValidateName first when handling user input.
func (m *Manager) PathsFor(agentName string) (Paths, error) {
	logDir := filepath.Join(m.HomeDir, ".shipyard", "logs", "crew")
	switch m.Platform {
	case PlatformDarwin:
		label := "com.shipyard.crew." + agentName
		return Paths{
			UnitFile: filepath.Join(m.HomeDir, "Library", "LaunchAgents", label+".plist"),
			LogDir:   logDir,
			OutLog:   filepath.Join(logDir, agentName+".out.log"),
			ErrLog:   filepath.Join(logDir, agentName+".err.log"),
			Label:    label,
		}, nil
	case PlatformLinux:
		label := "shipyard-crew-" + agentName + ".service"
		return Paths{
			UnitFile: filepath.Join(m.HomeDir, ".config", "systemd", "user", label),
			LogDir:   logDir,
			OutLog:   filepath.Join(logDir, agentName+".out.log"),
			ErrLog:   filepath.Join(logDir, agentName+".err.log"),
			Label:    label,
		}, nil
	default:
		return Paths{}, fmt.Errorf("%w: %s", ErrUnsupportedPlatform, m.Platform)
	}
}

// ValidateName returns nil if name is acceptable as a crew agent identifier.
// It is the only safety check between user input and shell argv, so callers
// must invoke it before any path or command construction with untrusted data.
func ValidateName(name string) error {
	if !agentNameRe.MatchString(name) {
		return fmt.Errorf("invalid agent name %q: must match %s", name, agentNameRe.String())
	}
	return nil
}

// RegisterAgentService writes the launchd plist (macOS) or systemd unit
// (Linux) for agentName, then loads it through the platform-native command.
// binaryPath must point at an executable on disk (typically the resolved
// path to `shipyard-crew`).
func (m *Manager) RegisterAgentService(ctx context.Context, agentName, binaryPath string) error {
	if err := ValidateName(agentName); err != nil {
		return err
	}
	if err := assertExecutable(binaryPath); err != nil {
		return err
	}
	paths, err := m.PathsFor(agentName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.LogDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.UnitFile), 0o755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}

	var rendered string
	switch m.Platform {
	case PlatformDarwin:
		rendered = renderLaunchdPlist(agentName, binaryPath, paths)
	case PlatformLinux:
		rendered = renderSystemdUnit(agentName, binaryPath, paths)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedPlatform, m.Platform)
	}

	if err := writeAtomic(paths.UnitFile, []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	return m.loadUnit(ctx, paths)
}

// UnregisterAgentService unloads the OS service and removes the unit file.
// Missing files / not-loaded errors are silenced — the operation is
// idempotent.
func (m *Manager) UnregisterAgentService(ctx context.Context, agentName string) error {
	if err := ValidateName(agentName); err != nil {
		return err
	}
	paths, err := m.PathsFor(agentName)
	if err != nil {
		return err
	}

	_ = m.unloadUnit(ctx, paths)

	if err := os.Remove(paths.UnitFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove unit file: %w", err)
	}

	if m.Platform == PlatformLinux {
		_, _ = m.Runner.Run(ctx, "systemctl", "--user", "daemon-reload")
	}
	return nil
}

func (m *Manager) loadUnit(ctx context.Context, paths Paths) error {
	switch m.Platform {
	case PlatformDarwin:
		domain := "gui/" + strconv.Itoa(m.UID)
		out, err := m.Runner.Run(ctx, "launchctl", "bootstrap", domain, paths.UnitFile)
		if err == nil {
			return nil
		}
		// Older macOS lacks bootstrap — fall back to load.
		out2, err2 := m.Runner.Run(ctx, "launchctl", "load", paths.UnitFile)
		if err2 == nil {
			return nil
		}
		return fmt.Errorf("launchctl bootstrap+load failed: %w; output=%s / %s", err, string(out), string(out2))
	case PlatformLinux:
		if out, err := m.Runner.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %w; output=%s", err, string(out))
		}
		if out, err := m.Runner.Run(ctx, "systemctl", "--user", "enable", "--now", paths.Label); err != nil {
			return fmt.Errorf("systemctl enable: %w; output=%s", err, string(out))
		}
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedPlatform, m.Platform)
	}
}

func (m *Manager) unloadUnit(ctx context.Context, paths Paths) error {
	switch m.Platform {
	case PlatformDarwin:
		domain := "gui/" + strconv.Itoa(m.UID) + "/" + paths.Label
		if _, err := m.Runner.Run(ctx, "launchctl", "bootout", domain); err == nil {
			return nil
		}
		_, err := m.Runner.Run(ctx, "launchctl", "unload", paths.UnitFile)
		return err
	case PlatformLinux:
		_, err := m.Runner.Run(ctx, "systemctl", "--user", "disable", "--now", paths.Label)
		return err
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedPlatform, m.Platform)
	}
}

func assertExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("binary path: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("binary path %q is a directory", path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("binary path %q is not executable", path)
	}
	return nil
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func renderLaunchdPlist(name, binary string, paths Paths) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n")
	b.WriteString("<dict>\n")
	b.WriteString("  <key>Label</key><string>" + paths.Label + "</string>\n")
	b.WriteString("  <key>ProgramArguments</key>\n")
	b.WriteString("  <array>\n")
	b.WriteString("    <string>" + binary + "</string>\n")
	b.WriteString("    <string>--service</string>\n")
	b.WriteString("    <string>--agent</string>\n")
	b.WriteString("    <string>" + name + "</string>\n")
	b.WriteString("  </array>\n")
	b.WriteString("  <key>RunAtLoad</key><true/>\n")
	b.WriteString("  <key>KeepAlive</key><true/>\n")
	b.WriteString("  <key>StandardOutPath</key><string>" + paths.OutLog + "</string>\n")
	b.WriteString("  <key>StandardErrorPath</key><string>" + paths.ErrLog + "</string>\n")
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.String()
}

func renderSystemdUnit(name, binary string, paths Paths) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Shipyard crew agent (" + name + ")\n")
	b.WriteString("After=network.target\n")
	b.WriteString("\n")
	b.WriteString("[Service]\n")
	b.WriteString("ExecStart=" + binary + " --service --agent " + name + "\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=2\n")
	b.WriteString("StandardOutput=append:" + paths.OutLog + "\n")
	b.WriteString("StandardError=append:" + paths.ErrLog + "\n")
	b.WriteString("\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}
