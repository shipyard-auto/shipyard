package fairway

import (
	"strconv"
	"sync"
	"time"
)

// StatsSnapshot describes in-memory runtime counters for the Fairway daemon.
type StatsSnapshot struct {
	RequestsTotal   int64            `json:"requestsTotal"`
	RequestsHandled int64            `json:"requestsHandled"`
	RouteNotFound   int64            `json:"routeNotFound"`
	AuthFailures    int64            `json:"authFailures"`
	ActionFailures  int64            `json:"actionFailures"`
	ActiveRequests  int64            `json:"activeRequests"`
	LastRequestAt   time.Time        `json:"lastRequestAt,omitempty"`
	StatusCodes     map[string]int64 `json:"statusCodes,omitempty"`
}

type statsProvider interface {
	Stats() StatsSnapshot
}

type runtimeStats struct {
	mu sync.RWMutex

	requestsTotal   int64
	requestsHandled int64
	routeNotFound   int64
	authFailures    int64
	actionFailures  int64
	activeRequests  int64
	lastRequestAt   time.Time
	statusCodes     map[string]int64
}

func newRuntimeStats() *runtimeStats {
	return &runtimeStats{statusCodes: make(map[string]int64)}
}

func (s *runtimeStats) Begin(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestsTotal++
	s.activeRequests++
	s.lastRequestAt = now.UTC()
}

func (s *runtimeStats) RecordHandled(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestsHandled++
	s.statusCodes[strconv.Itoa(status)]++
}

func (s *runtimeStats) RecordRouteNotFound(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routeNotFound++
	s.statusCodes[strconv.Itoa(status)]++
}

func (s *runtimeStats) RecordAuthFailure(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authFailures++
	s.statusCodes[strconv.Itoa(status)]++
}

func (s *runtimeStats) RecordActionFailure(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actionFailures++
	s.statusCodes[strconv.Itoa(status)]++
}

func (s *runtimeStats) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeRequests > 0 {
		s.activeRequests--
	}
}

func (s *runtimeStats) Stats() StatsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	statusCodes := make(map[string]int64, len(s.statusCodes))
	for key, value := range s.statusCodes {
		statusCodes[key] = value
	}

	return StatsSnapshot{
		RequestsTotal:   s.requestsTotal,
		RequestsHandled: s.requestsHandled,
		RouteNotFound:   s.routeNotFound,
		AuthFailures:    s.authFailures,
		ActionFailures:  s.actionFailures,
		ActiveRequests:  s.activeRequests,
		LastRequestAt:   s.lastRequestAt,
		StatusCodes:     statusCodes,
	}
}
