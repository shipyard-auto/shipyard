package logs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type ConfigStore struct {
	Path string
}

func NewConfigStore() (ConfigStore, error) {
	configPath, _, err := DefaultPaths()
	if err != nil {
		return ConfigStore{}, err
	}

	return ConfigStore{Path: configPath}, nil
}

func (s ConfigStore) Load() (Config, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg := Config{RetentionDays: DefaultRetentionDays}
			if err := s.Save(cfg); err != nil {
				return Config{}, err
			}
			return cfg, nil
		}
		return Config{}, fmt.Errorf("read logs config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse logs config: %w", err)
	}
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = DefaultRetentionDays
	}
	return cfg, nil
}

func (s ConfigStore) Save(cfg Config) error {
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = DefaultRetentionDays
	}

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("create logs config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal logs config: %w", err)
	}

	if err := os.WriteFile(s.Path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write logs config: %w", err)
	}
	return nil
}
