//go:build !linux

package service

import "fmt"

func newSystemdManager() (Manager, error) {
	return nil, fmt.Errorf("systemd manager is only available on linux")
}
