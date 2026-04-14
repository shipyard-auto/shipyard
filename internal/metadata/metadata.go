package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const installFilename = "install.json"

type InstallManifest struct {
	Version     string    `json:"version"`
	BinaryPath  string    `json:"binary_path"`
	HomeDir     string    `json:"home_dir"`
	InstalledAt time.Time `json:"installed_at"`
}

func DefaultHomeDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}

	return filepath.Join(homeDir, ".shipyard"), nil
}

func ManifestPath(homeDir string) string {
	return filepath.Join(homeDir, installFilename)
}

func Write(homeDir string, manifest InstallManifest) error {
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return fmt.Errorf("create shipyard home directory: %w", err)
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal install manifest: %w", err)
	}

	if err := os.WriteFile(ManifestPath(homeDir), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write install manifest: %w", err)
	}

	return nil
}

func Read(homeDir string) (InstallManifest, error) {
	data, err := os.ReadFile(ManifestPath(homeDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return InstallManifest{}, err
		}

		return InstallManifest{}, fmt.Errorf("read install manifest: %w", err)
	}

	var manifest InstallManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return InstallManifest{}, fmt.Errorf("parse install manifest: %w", err)
	}

	return manifest, nil
}
