package service

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var envKeyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
var idPattern = regexp.MustCompile(`^[A-Z0-9]{6}$`)

func validateAddInput(input ServiceInput) error {
	if strings.TrimSpace(derefString(input.Name)) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(derefString(input.Command)) == "" {
		return errors.New("command is required")
	}
	return validateServiceFields(
		strings.TrimSpace(derefString(input.Name)),
		strings.TrimSpace(derefString(input.Description)),
		strings.TrimSpace(derefString(input.Command)),
		strings.TrimSpace(derefString(input.WorkingDir)),
		derefEnvironment(input.Environment),
	)
}

func validateStoredService(record ServiceRecord) error {
	if !idPattern.MatchString(record.ID) {
		return errors.New("id must be 6 uppercase letters or digits")
	}
	return validateServiceFields(record.Name, record.Description, record.Command, record.WorkingDir, record.Environment)
}

func validateStore(store Store) error {
	seen := make(map[string]struct{}, len(store.Services))
	for _, record := range store.Services {
		if _, exists := seen[record.ID]; exists {
			return fmt.Errorf("duplicate service id: %s", record.ID)
		}
		seen[record.ID] = struct{}{}
		if err := validateStoredService(record); err != nil {
			return fmt.Errorf("invalid service %s: %w", record.ID, err)
		}
	}
	return nil
}

func validateServiceFields(name, description, command, workingDir string, environment map[string]string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	if strings.Contains(name, "\n") {
		return errors.New("name must be a single line")
	}
	if strings.Contains(description, "\n") {
		return errors.New("description must be a single line")
	}
	if strings.TrimSpace(command) == "" {
		return errors.New("command is required")
	}
	if strings.Contains(command, "\n") {
		return errors.New("command must be a single line")
	}
	if workingDir != "" {
		if strings.Contains(workingDir, "\n") {
			return errors.New("workingDir must be a single line")
		}
		if !strings.HasPrefix(workingDir, "/") {
			return errors.New("workingDir must be an absolute path")
		}
	}
	for key, value := range environment {
		if !envKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid environment key: %s", key)
		}
		if strings.Contains(value, "\n") {
			return fmt.Errorf("environment value for %s must be a single line", key)
		}
	}
	return nil
}
