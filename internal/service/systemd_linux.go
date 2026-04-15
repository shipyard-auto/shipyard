//go:build linux

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

type systemdManager struct {
	exec    func(name string, args ...string) *exec.Cmd
	homeDir string
	unitDir string
}

func newSystemdManager() (Manager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home: %w", err)
	}
	return &systemdManager{
		exec:    exec.Command,
		homeDir: homeDir,
		unitDir: filepath.Join(homeDir, ".config", "systemd", "user"),
	}, nil
}

func (m *systemdManager) Platform() Platform { return PlatformSystemd }

func (m *systemdManager) Sync(desired []ServiceRecord) error {
	if err := os.MkdirAll(m.unitDir, 0o755); err != nil {
		return fmt.Errorf("create systemd user unit directory: %w", err)
	}
	desiredNames := make(map[string]ServiceRecord, len(desired))
	for _, record := range desired {
		name := systemdUnitName(record.ID)
		desiredNames[name] = record
		path := filepath.Join(m.unitDir, name)
		rendered := renderSystemdUnit(withDefaultEnvironment(record, m.homeDir))
		if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
			return fmt.Errorf("write systemd unit: %w", err)
		}
	}
	entries, err := os.ReadDir(m.unitDir)
	if err != nil {
		return fmt.Errorf("read systemd unit directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "shipyard-") || !strings.HasSuffix(entry.Name(), ".service") {
			continue
		}
		if _, ok := desiredNames[entry.Name()]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(m.unitDir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove orphaned systemd unit: %w", err)
		}
	}
	return nil
}

func (m *systemdManager) Reload() error {
	_, err := m.run("systemctl", "--user", "daemon-reload")
	return err
}

func (m *systemdManager) Start(id string) error {
	_, err := m.run("systemctl", "--user", "start", systemdUnitName(id))
	return err
}
func (m *systemdManager) Stop(id string) error {
	_, err := m.run("systemctl", "--user", "stop", systemdUnitName(id))
	return err
}
func (m *systemdManager) Restart(id string) error {
	_, err := m.run("systemctl", "--user", "restart", systemdUnitName(id))
	return err
}
func (m *systemdManager) Enable(id string) error {
	_, err := m.run("systemctl", "--user", "enable", systemdUnitName(id))
	return err
}
func (m *systemdManager) Disable(id string) error {
	_, err := m.run("systemctl", "--user", "disable", systemdUnitName(id))
	return err
}

func (m *systemdManager) Remove(id string) error {
	_ = m.Disable(id)
	_ = m.Stop(id)
	path := filepath.Join(m.unitDir, systemdUnitName(id))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove systemd unit: %w", err)
	}
	return nil
}

func (m *systemdManager) Status(id string) (RuntimeStatus, error) {
	output, err := m.run("systemctl", "--user", "show", systemdUnitName(id), "--property=ActiveState,SubState,MainPID,UnitFileState,ExecMainStatus,ActiveEnterTimestamp")
	if err != nil {
		return RuntimeStatus{}, err
	}
	status := RuntimeStatus{State: "unknown", EnabledAt: "unknown", Raw: output}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			status.State = value
		case "SubState":
			status.SubState = value
		case "MainPID":
			status.PID, _ = strconv.Atoi(strings.TrimSpace(value))
		case "UnitFileState":
			if value == "enabled" {
				status.EnabledAt = "login"
			} else if value != "" {
				status.EnabledAt = "off"
			}
		case "ExecMainStatus":
			status.LastExit, _ = strconv.Atoi(strings.TrimSpace(value))
		case "ActiveEnterTimestamp":
			if strings.TrimSpace(value) != "" {
				status.SinceHint = value
			}
		}
	}
	return status, nil
}

func (m *systemdManager) run(name string, args ...string) (string, error) {
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

var _ Manager = (*systemdManager)(nil)
