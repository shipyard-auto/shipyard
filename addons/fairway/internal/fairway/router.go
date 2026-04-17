package fairway

import (
	"errors"
	"fmt"
	"slices"
	"sync"
)

// ErrRouteNotFound is returned by Delete and Replace when the target path does
// not exist in the routing table.
var ErrRouteNotFound = errors.New("route not found")

// Router is the in-memory routing table for the fairway daemon.
// It is the single source of truth during the daemon's lifetime.
// Concurrent reads are allowed; writes are exclusive and always persisted
// to disk before updating memory.
type Router struct {
	mu     sync.RWMutex
	config Config
	repo   Repository
}

// NewRouter creates a Router by loading the initial config from repo.
// Returns an error if loading or validating the config fails.
func NewRouter(repo Repository) (*Router, error) {
	cfg, err := repo.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &Router{config: cfg, repo: repo}, nil
}

// NewRouterWithConfig creates a Router with an already-loaded config.
// Intended for tests that need to bypass disk I/O on construction.
func NewRouterWithConfig(repo Repository, cfg Config) *Router {
	return &Router{config: cfg, repo: repo}
}

// Config returns a snapshot of the current daemon configuration.
// The returned value is a copy; callers must not mutate it.
func (r *Router) Config() Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config
}

// List returns a clone of the current route slice.
// Mutating the returned slice does not affect the routing table.
func (r *Router) List() []Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.config.Routes)
}

// Match performs an exact-path lookup against the routing table.
// It returns the matching Route and true, or a zero Route and false.
func (r *Router) Match(path string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, route := range r.config.Routes {
		if route.Path == path {
			return route, true
		}
	}
	return Route{}, false
}

// Add validates route, persists the updated config, then appends it to memory.
// Returns ErrDuplicateRoutePath if the path already exists.
// If Save fails, memory is left unchanged.
func (r *Router) Add(route Route) error {
	if err := route.Validate(); err != nil {
		return fmt.Errorf("invalid route: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, existing := range r.config.Routes {
		if existing.Path == route.Path {
			return fmt.Errorf("%w: %q", ErrDuplicateRoutePath, route.Path)
		}
	}

	newRoutes := append(slices.Clone(r.config.Routes), route)
	newConfig := r.config
	newConfig.Routes = newRoutes

	if err := r.repo.Save(newConfig); err != nil {
		return fmt.Errorf("persist add: %w", err)
	}

	r.config = newConfig
	return nil
}

// Delete removes the route with the given path and persists the change.
// Returns ErrRouteNotFound if no route with that path exists.
// If Save fails, memory is left unchanged.
func (r *Router) Delete(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	routes := r.config.Routes
	idx := -1
	for i, route := range routes {
		if route.Path == path {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("%w: %q", ErrRouteNotFound, path)
	}

	newRoutes := slices.Delete(slices.Clone(routes), idx, idx+1)
	newConfig := r.config
	newConfig.Routes = newRoutes

	if err := r.repo.Save(newConfig); err != nil {
		return fmt.Errorf("persist delete: %w", err)
	}

	r.config = newConfig
	return nil
}

// Replace finds the route with route.Path and replaces it in-place,
// preserving the order of all other routes.
// Returns ErrRouteNotFound if the path does not exist.
// If Save fails, memory is left unchanged.
func (r *Router) Replace(route Route) error {
	if err := route.Validate(); err != nil {
		return fmt.Errorf("invalid route: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	routes := r.config.Routes
	idx := -1
	for i, existing := range routes {
		if existing.Path == route.Path {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("%w: %q", ErrRouteNotFound, route.Path)
	}

	newRoutes := slices.Clone(routes)
	newRoutes[idx] = route
	newConfig := r.config
	newConfig.Routes = newRoutes

	if err := r.repo.Save(newConfig); err != nil {
		return fmt.Errorf("persist replace: %w", err)
	}

	r.config = newConfig
	return nil
}
