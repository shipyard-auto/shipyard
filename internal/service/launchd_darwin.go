//go:build darwin

package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type launchdManager struct {
	exec     func(name string, args ...string) *exec.Cmd
	uid      int
	agentsDir string
}

func newLaunchdManager() (Manager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home: %w", err)
	}
	return &launchdManager{
		exec:      exec.Command,
		uid:       os.Getuid(),
		agentsDir: filepath.Join(homeDir, "Library", "LaunchAgents"),
	}, nil
}

func (m *launchdManager) Platform() Platform { return PlatformLaunchd }

func (m *launchdManager) Sync(desired []ServiceRecord) error {
	if err := os.MkdirAll(m.agentsDir, 0o755); err != nil {
		return fmt.Errorf("create launch agents directory: %w", err)
	}
	desiredNames := make(map[string]ServiceRecord, len(desired))
	for _, record := range desired {
		name := launchdPlistName(record.ID)
		label := launchdLabel(record.ID)
		path := filepath.Join(m.agentsDir, name)
		_, _ = m.run("launchctl", "bootout", m.domain()+"/"+label)
		if err := os.WriteFile(path, []byte(renderLaunchdPlist(record)), 0o644); err != nil {
			return fmt.Errorf("write launchd plist: %w", err)
		}
		if record.Enabled {
			if _, err := m.run("launchctl", "bootstrap", m.domain(), path); err != nil {
				return err
			}
			if _, err := m.run("launchctl", "enable", m.domain()+"/"+label); err != nil {
				return err
			}
		}
		desiredNames[name] = record
	}
	entries, err := os.ReadDir(m.agentsDir)
	if err != nil {
		return fmt.Errorf("read launch agents directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "com.shipyard.service.") || !strings.HasSuffix(entry.Name(), ".plist") {
			continue
		}
		if _, ok := desiredNames[entry.Name()]; ok {
			continue
		}
		label := strings.TrimSuffix(entry.Name(), ".plist")
		_, _ = m.run("launchctl", "bootout", m.domain()+"/"+label)
		if err := os.Remove(filepath.Join(m.agentsDir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove orphaned launchd plist: %w", err)
		}
	}
	return nil
}

func (m *launchdManager) Reload() error { return nil }

func (m *launchdManager) Start(id string) error {
	_, err := m.run("launchctl", "kickstart", "-k", m.domain()+"/"+launchdLabel(id))
	return err
}

func (m *launchdManager) Stop(id string) error {
	_, err := m.run("launchctl", "bootout", m.domain()+"/"+launchdLabel(id))
	return err
}

func (m *launchdManager) Restart(id string) error {
	_ = m.Stop(id)
	return m.Start(id)
}

func (m *launchdManager) Enable(id string) error {
	_, err := m.run("launchctl", "enable", m.domain()+"/"+launchdLabel(id))
	if err != nil {
		return err
	}
	_, err = m.run("launchctl", "bootstrap", m.domain(), filepath.Join(m.agentsDir, launchdPlistName(id)))
	return err
}

func (m *launchdManager) Disable(id string) error {
	_, _ = m.run("launchctl", "bootout", m.domain()+"/"+launchdLabel(id))
	_, err := m.run("launchctl", "disable", m.domain()+"/"+launchdLabel(id))
	return err
}

func (m *launchdManager) Remove(id string) error {
	_ = m.Disable(id)
	path := filepath.Join(m.agentsDir, launchdPlistName(id))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove launchd plist: %w", err)
	}
	return nil
}

func (m *launchdManager) Status(id string) (RuntimeStatus, error) {
	output, err := m.run("launchctl", "print", m.domain()+"/"+launchdLabel(id))
	if err != nil {
		return RuntimeStatus{}, err
	}
	status := RuntimeStatus{State: "unknown", EnabledAt: "unknown", Raw: output}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "state = "):
			status.State = strings.TrimPrefix(trimmed, "state = ")
		case strings.HasPrefix(trimmed, "pid = "):
			status.PID, _ = strconv.Atoi(strings.TrimPrefix(trimmed, "pid = "))
		case strings.HasPrefix(trimmed, "last exit code = "):
			status.LastExit, _ = strconv.Atoi(strings.TrimPrefix(trimmed, "last exit code = "))
		case strings.HasPrefix(trimmed, "active count = "):
			if status.State == "unknown" {
				if strings.TrimPrefix(trimmed, "active count = ") == "0" {
					status.State = "inactive"
				} else {
					status.State = "active"
				}
			}
		}
	}
	status.EnabledAt = "login"
	return status, nil
}

func (m *launchdManager) domain() string {
	return fmt.Sprintf("gui/%d", m.uid)
}

func (m *launchdManager) run(name string, args ...string) (string, error) {
	cmd := m.exec(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(output))
		}
		return string(output), fmt.Errorf("%s: %w: %s", strings.Join(append([]string{name}, args...), " "), err, msg)
	}
	return strings.TrimSpace(string(output)), nil
}

var _ Manager = (*launchdManager)(nil)

