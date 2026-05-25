package app

import (
	"sync"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms/provider"
	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms/provider/telegram"
)

// defaultRegistry is the process-wide provider catalog. Built lazily so
// tests can call Providers() without pulling the production HTTP client
// at package-init time, and so the registry can be swapped via
// SetProviders() in fuller integration tests.
var (
	registryOnce sync.Once
	registry     *provider.Registry
	registryMu   sync.RWMutex
)

// Providers returns the process-wide Registry, populated with the v1
// production providers (telegram) on first call. Subsequent calls return
// the same Registry until SetProviders replaces it.
func Providers() *provider.Registry {
	registryMu.RLock()
	if registry != nil {
		defer registryMu.RUnlock()
		return registry
	}
	registryMu.RUnlock()

	registryOnce.Do(func() {
		registryMu.Lock()
		defer registryMu.Unlock()
		r := provider.NewRegistry()
		r.Register(comms.ChannelTypeTelegram, telegram.New())
		registry = r
	})

	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry
}

// SetProviders replaces the process-wide registry. Intended for tests
// that need to inject a fake provider; production code should not call
// this.
func SetProviders(r *provider.Registry) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = r
	// Mark Once as consumed so future Providers() calls skip the default
	// initialiser and return r as-is.
	registryOnce.Do(func() {})
}
