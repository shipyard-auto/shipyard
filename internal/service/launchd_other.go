//go:build !darwin

package service

import "fmt"

func newLaunchdManager() (Manager, error) {
	return nil, fmt.Errorf("launchd manager is only available on darwin")
}
