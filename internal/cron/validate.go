package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func validateStore(store Store) error {
	seen := make(map[string]struct{}, len(store.Jobs))
	for _, job := range store.Jobs {
		if _, exists := seen[job.ID]; exists {
			return fmt.Errorf("duplicate cron job id: %s", job.ID)
		}
		seen[job.ID] = struct{}{}

		if err := validateStoredJob(job); err != nil {
			return fmt.Errorf("invalid job %s: %w", job.ID, err)
		}
	}

	return nil
}

func validateAddInput(input JobInput) error {
	if strings.TrimSpace(derefString(input.Name)) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(derefString(input.Schedule)) == "" {
		return errors.New("schedule is required")
	}
	if strings.TrimSpace(derefString(input.Command)) == "" {
		return errors.New("command is required")
	}

	return validateJobFields(
		strings.TrimSpace(derefString(input.Name)),
		strings.TrimSpace(derefString(input.Schedule)),
		strings.TrimSpace(derefString(input.Command)),
	)
}

func validateStoredJob(job Job) error {
	return validateJobFields(job.Name, job.Schedule, job.Command)
}

func validateJobFields(name, schedule, command string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	if strings.Contains(name, "\n") {
		return errors.New("name must be a single line")
	}
	if strings.TrimSpace(schedule) == "" {
		return errors.New("schedule is required")
	}
	if err := validateSchedule(schedule); err != nil {
		return err
	}
	if strings.TrimSpace(command) == "" {
		return errors.New("command is required")
	}
	if strings.Contains(command, "\n") {
		return errors.New("command must be a single line")
	}

	return nil
}

func validateSchedule(schedule string) error {
	fields := strings.Fields(strings.TrimSpace(schedule))
	if len(fields) != 5 {
		return errors.New("schedule must have exactly 5 fields")
	}

	ranges := [][2]int{
		{0, 59},
		{0, 23},
		{1, 31},
		{1, 12},
		{0, 7},
	}

	for i, field := range fields {
		if err := validateCronField(field, ranges[i][0], ranges[i][1]); err != nil {
			return fmt.Errorf("invalid schedule field %d (%q): %w", i+1, field, err)
		}
	}

	return nil
}

func validateCronField(field string, min, max int) error {
	for _, segment := range strings.Split(field, ",") {
		if err := validateCronSegment(segment, min, max); err != nil {
			return err
		}
	}
	return nil
}

func validateCronSegment(segment string, min, max int) error {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return errors.New("empty segment")
	}

	stepParts := strings.Split(segment, "/")
	if len(stepParts) > 2 {
		return errors.New("too many step separators")
	}

	base := stepParts[0]
	if len(stepParts) == 2 {
		step, err := strconv.Atoi(stepParts[1])
		if err != nil || step <= 0 {
			return errors.New("step must be a positive integer")
		}
	}

	if base == "*" {
		return nil
	}

	rangeParts := strings.Split(base, "-")
	switch len(rangeParts) {
	case 1:
		return validateCronValue(rangeParts[0], min, max)
	case 2:
		if err := validateCronValue(rangeParts[0], min, max); err != nil {
			return err
		}
		if err := validateCronValue(rangeParts[1], min, max); err != nil {
			return err
		}

		start, _ := strconv.Atoi(rangeParts[0])
		end, _ := strconv.Atoi(rangeParts[1])
		if start > end {
			return errors.New("range start must be <= range end")
		}
		return nil
	default:
		return errors.New("too many range separators")
	}
}

func validateCronValue(value string, min, max int) error {
	if value == "*" {
		return nil
	}

	number, err := strconv.Atoi(value)
	if err != nil {
		return errors.New("must be an integer or *")
	}
	if number < min || number > max {
		return fmt.Errorf("must be between %d and %d", min, max)
	}
	return nil
}
