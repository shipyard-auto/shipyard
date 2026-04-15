package service

import (
	"fmt"
	"runtime"
)

type Manager interface {
	Platform() Platform
	Sync(desired []ServiceRecord) error
	Reload() error
	Start(id string) error
	Stop(id string) error
	Restart(id string) error
	Status(id string) (RuntimeStatus, error)
	Enable(id string) error
	Disable(id string) error
	Remove(id string) error
}

func NewManager() (Manager, error) {
	switch runtime.GOOS {
	case "linux":
		return newSystemdManager()
	case "darwin":
		return newLaunchdManager()
	default:
		return nil, fmt.Errorf("unsupported platform for shipyard service: %s", runtime.GOOS)
	}
}
