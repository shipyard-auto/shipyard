package fairway

import (
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrRouteNotFound indicates that a route mutation targeted a path that is not present in memory.
	ErrRouteNotFound = errors.New("route not found")
)

// Router keeps the in-memory routing table used by the Fairway daemon at runtime.
type Router struct {
	mu     sync.RWMutex
	config Config
	repo   Repository
}

// NewRouter loads the persisted config from the repository and initializes the in-memory router state.
func NewRouter(repo Repository) (*Router, error) {
	cfg, err := repo.Load()
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate router config: %w", err)
	}
	return &Router{
		config: cloneConfig(cfg),
		repo:   repo,
	}, nil
}

// NewRouterWithConfig builds a router from an explicit config snapshot.
func NewRouterWithConfig(repo Repository, cfg Config) *Router {
	return &Router{
		config: cloneConfig(cfg),
		repo:   repo,
	}
}

// Config returns a cloned snapshot of the current router config.
func (r *Router) Config() Config {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return cloneConfig(r.config)
}

// List returns a cloned snapshot of the registered routes.
func (r *Router) List() []Route {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return cloneRoutes(r.config.Routes)
}

// Match returns the route that exactly matches the provided path.
func (r *Router) Match(path string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, route := range r.config.Routes {
		if route.Path == path {
			return cloneRoute(route), true
		}
	}

	return Route{}, false
}

// Add validates, persists, and appends a new route to the in-memory table.
func (r *Router) Add(route Route) error {
	if err := route.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, existing := range r.config.Routes {
		if existing.Path == route.Path {
			return fmt.Errorf("%w: %q", ErrDuplicateRoutePath, route.Path)
		}
	}

	next := cloneConfig(r.config)
	next.Routes = append(next.Routes, cloneRoute(route))
	if err := r.repo.Save(next); err != nil {
		return err
	}

	r.config = next
	return nil
}

// Delete validates existence, persists, and removes a route from the in-memory table.
func (r *Router) Delete(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	index := -1
	for i, route := range r.config.Routes {
		if route.Path == path {
			index = i
			break
		}
	}
	if index == -1 {
		return fmt.Errorf("%w: %q", ErrRouteNotFound, path)
	}

	next := cloneConfig(r.config)
	next.Routes = append(next.Routes[:index], next.Routes[index+1:]...)
	if err := r.repo.Save(next); err != nil {
		return err
	}

	r.config = next
	return nil
}

// Replace validates, persists, and updates a route in place while preserving order.
func (r *Router) Replace(route Route) error {
	if err := route.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	index := -1
	for i, existing := range r.config.Routes {
		if existing.Path == route.Path {
			index = i
			break
		}
	}
	if index == -1 {
		return fmt.Errorf("%w: %q", ErrRouteNotFound, route.Path)
	}

	next := cloneConfig(r.config)
	next.Routes[index] = cloneRoute(route)
	if err := r.repo.Save(next); err != nil {
		return err
	}

	r.config = next
	return nil
}

func cloneConfig(cfg Config) Config {
	cloned := cfg
	cloned.Routes = cloneRoutes(cfg.Routes)
	return cloned
}

func cloneRoutes(routes []Route) []Route {
	if routes == nil {
		return nil
	}

	cloned := make([]Route, len(routes))
	for i, route := range routes {
		cloned[i] = cloneRoute(route)
	}
	return cloned
}

func cloneRoute(route Route) Route {
	cloned := route
	cloned.Action = cloneAction(route.Action)
	return cloned
}

func cloneAction(action Action) Action {
	cloned := action
	if action.Headers != nil {
		cloned.Headers = make(map[string]string, len(action.Headers))
		for key, value := range action.Headers {
			cloned.Headers[key] = value
		}
	}
	return cloned
}
