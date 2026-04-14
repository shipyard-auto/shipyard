package system

import (
	"errors"
	"fmt"
	"os"
)

func RemoveFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove file %q: %w", path, err)
	}

	return nil
}

func RemoveAll(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove path %q: %w", path, err)
	}

	return nil
}
