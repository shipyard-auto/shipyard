package fairway

import (
	"sync"
	"sync/atomic"
	"time"
)

// routeStats holds per-route counters.
type routeStats struct {
	Count    int64
	ErrCount int64
	LastAt   time.Time
}

// RouteStats is the exported snapshot of per-route counters.
type RouteStats struct {
	Count    int64
	ErrCount int64
	LastAt   time.Time
}

// StatsSnapshot is a point-in-time copy of all stats, safe to read without locks.
type StatsSnapshot struct {
	Total      int64
	ByRoute    map[string]RouteStats
	ByStatus   map[int]int64
	ByExitCode map[int]int64
	StartedAt  time.Time
}

// Stats is a thread-safe request counter. Used by the socket `stats` method.
type Stats struct {
	mu         sync.Mutex
	total      int64
	byRoute    map[string]*routeStats
	byStatus   map[int]int64
	byExitCode map[int]int64
	startedAt  time.Time
}

// NewStats creates a new Stats instance with the given start time.
func NewStats(startedAt time.Time) *Stats {
	return &Stats{
		byRoute:    make(map[string]*routeStats),
		byStatus:   make(map[int]int64),
		byExitCode: make(map[int]int64),
		startedAt:  startedAt,
	}
}

// Observe records a single request.
// path is the route path, status is the HTTP status code, duration is unused in V1.
func (s *Stats) Observe(path string, status int, duration time.Duration) {
	s.ObserveResult(path, status, -1, duration)
}

// ObserveResult records a request including the action exit code when known.
func (s *Stats) ObserveResult(path string, status int, exitCode int, duration time.Duration) {
	atomic.AddInt64(&s.total, 1)

	s.mu.Lock()
	defer s.mu.Unlock()

	rs, ok := s.byRoute[path]
	if !ok {
		rs = &routeStats{}
		s.byRoute[path] = rs
	}
	rs.Count++
	if status >= 500 {
		rs.ErrCount++
	}
	rs.LastAt = time.Now()

	s.byStatus[status]++
	s.byExitCode[exitCode]++
}

// Snapshot returns a deep copy of the current stats.
func (s *Stats) Snapshot() StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	byRoute := make(map[string]RouteStats, len(s.byRoute))
	for k, v := range s.byRoute {
		byRoute[k] = RouteStats{
			Count:    v.Count,
			ErrCount: v.ErrCount,
			LastAt:   v.LastAt,
		}
	}

	byStatus := make(map[int]int64, len(s.byStatus))
	for k, v := range s.byStatus {
		byStatus[k] = v
	}

	byExitCode := make(map[int]int64, len(s.byExitCode))
	for k, v := range s.byExitCode {
		byExitCode[k] = v
	}

	return StatsSnapshot{
		Total:      atomic.LoadInt64(&s.total),
		ByRoute:    byRoute,
		ByStatus:   byStatus,
		ByExitCode: byExitCode,
		StartedAt:  s.startedAt,
	}
}
