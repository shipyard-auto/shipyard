package cron

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Crontab interface {
	Read() (string, error)
	Write(string) error
}

type SystemCrontab struct{}

func (SystemCrontab) Read() (string, error) {
	cmd := exec.Command("crontab", "-l")
	output, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			text := strings.ToLower(string(output))
			if exitErr.ExitCode() == 1 && (strings.Contains(text, "no crontab") || strings.TrimSpace(text) == "") {
				return "", nil
			}
		}
		return "", fmt.Errorf("read user crontab: %w", err)
	}

	return string(output), nil
}

func (SystemCrontab) Write(contents string) error {
	tempFile, err := os.CreateTemp("", "shipyard-crontab-*")
	if err != nil {
		return fmt.Errorf("create temporary crontab file: %w", err)
	}

	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if _, err := tempFile.WriteString(contents); err != nil {
		tempFile.Close()
		return fmt.Errorf("write temporary crontab file: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temporary crontab file: %w", err)
	}

	cmd := exec.Command("crontab", tempPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("install user crontab: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}
