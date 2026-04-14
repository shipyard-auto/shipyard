package cron

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shipyard-auto/shipyard/internal/metadata"
)

const storeFilename = "crons.json"

type Repository interface {
	Load() (Store, error)
	Save(Store) error
}

type FileRepository struct {
	Path string
}

func DefaultStorePath() (string, error) {
	shipyardHome, err := metadata.DefaultHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(shipyardHome, storeFilename), nil
}

func NewFileRepository() (FileRepository, error) {
	path, err := DefaultStorePath()
	if err != nil {
		return FileRepository{}, err
	}

	return FileRepository{Path: path}, nil
}

func (r FileRepository) Load() (Store, error) {
	data, err := os.ReadFile(r.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			store := Store{Notice: storeNotice, Version: storeVersion, Jobs: []Job{}}
			if err := r.Save(store); err != nil {
				return Store{}, err
			}
			return store, nil
		}

		return Store{}, fmt.Errorf("read cron store: %w", err)
	}

	var store Store
	if err := json.Unmarshal(data, &store); err != nil {
		return Store{}, fmt.Errorf("parse cron store: %w", err)
	}

	if store.Version == 0 {
		store.Version = storeVersion
	}
	if store.Notice == "" {
		store.Notice = storeNotice
	}
	if store.Jobs == nil {
		store.Jobs = []Job{}
	}

	return store, nil
}

func (r FileRepository) Save(store Store) error {
	store.Version = storeVersion
	store.Notice = storeNotice
	if store.Jobs == nil {
		store.Jobs = []Job{}
	}

	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return fmt.Errorf("create cron store directory: %w", err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cron store: %w", err)
	}

	if err := os.WriteFile(r.Path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write cron store: %w", err)
	}

	return nil
}
