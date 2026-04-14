package uninstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shipyard-auto/shipyard/internal/metadata"
	"github.com/shipyard-auto/shipyard/internal/system"
)

type FileRemover interface {
	RemoveFile(path string) error
	RemoveAll(path string) error
}

type filesystemRemover struct{}

func (filesystemRemover) RemoveFile(path string) error {
	return system.RemoveFile(path)
}

func (filesystemRemover) RemoveAll(path string) error {
	return system.RemoveAll(path)
}

type Service struct {
	Remover FileRemover
}

type Result struct {
	BinaryPath      string
	BinaryRemoved   bool
	HomeDir         string
	HomeDirRemoved  bool
	ManifestPresent bool
}

func NewService() Service {
	return Service{Remover: filesystemRemover{}}
}

func (s Service) Run(explicitBinaryPath string) (Result, error) {
	shipyardHome, err := metadata.DefaultHomeDir()
	if err != nil {
		return Result{}, err
	}

	result := Result{
		HomeDir: shipyardHome,
	}

	manifest, err := metadata.Read(shipyardHome)
	if err == nil {
		result.ManifestPresent = true
		result.BinaryPath = manifest.BinaryPath
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("load install manifest: %w", err)
	}

	if result.BinaryPath == "" {
		result.BinaryPath = explicitBinaryPath
	}

	if result.BinaryPath != "" {
		if err := s.Remover.RemoveFile(result.BinaryPath); err != nil {
			return Result{}, err
		}
		result.BinaryRemoved = fileMissing(result.BinaryPath)
	}

	if err := s.Remover.RemoveAll(result.HomeDir); err != nil {
		return Result{}, err
	}
	result.HomeDirRemoved = fileMissing(result.HomeDir)

	return result, nil
}

func fileMissing(path string) bool {
	_, err := os.Stat(filepath.Clean(path))
	return errors.Is(err, os.ErrNotExist)
}
