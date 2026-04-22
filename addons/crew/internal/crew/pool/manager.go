// Package pool implements the concurrency manager used by the crew runner to
// gate agent executions behind named pools with a bounded wait queue.
//
// Each pool is a counting semaphore (buffered channel) of fixed capacity.
// Acquire tries a non-blocking send; on contention it either fails fast
// (strategy "reject") or enqueues up to max_queue_size waiters and blocks
// until a slot frees, ctx is cancelled or max_wait elapses. Slots are
// released idempotently via Slot.Release.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/config"
)

// Strategy names accepted by the manager. They mirror config.QueueStrategy
// but live here too so callers that only depend on pool need not import
// config.
const (
	StrategyWait   = "wait"
	StrategyReject = "reject"
)

// Sentinel errors returned by Acquire. Callers should use errors.Is.
var (
	ErrPoolFull       = errors.New("pool full")
	ErrQueueFull      = errors.New("queue full")
	ErrAcquireTimeout = errors.New("acquire timeout")
	ErrUnknownPool    = errors.New("unknown pool")
)

// Slot represents one acquired unit of concurrency in a pool. Release must be
// called exactly once per successful Acquire; repeated calls are safe and
// produce no effect.
type Slot struct {
	release func()
}

// Release frees the underlying pool slot. Safe to call on a nil Slot and
// safe to call multiple times.
func (s *Slot) Release() {
	if s == nil || s.release == nil {
		return
	}
	s.release()
}

type pool struct {
	name    string
	sem     chan struct{}
	waiting atomic.Int64

	acquired atomic.Int64
	rejected atomic.Int64
	queued   atomic.Int64
	timedOut atomic.Int64
}

// Manager owns the set of named pools and enforces the global queue policy
// (strategy, max_wait, max_queue_size) configured in config.ConcurrencyConfig.
type Manager struct {
	pools        map[string]*pool
	queueStrat   string
	maxWait      time.Duration
	maxQueueSize int
}

// Stats is a point-in-time snapshot of a single pool. Counters are cumulative
// since Manager construction.
type Stats struct {
	Pool     string
	Size     int
	InUse    int
	Waiting  int
	Acquired int64
	Rejected int64
	Queued   int64
	TimedOut int64
}

const (
	defaultStrategy     = StrategyWait
	defaultMaxWait      = 30 * time.Second
	defaultMaxQueueSize = 16
)

// NewManager builds a Manager from the given concurrency config. A nil cfg
// yields a Manager with no pools and defaults for the queue policy. Pools
// with Max<=0 are normalised to 1.
func NewManager(cfg *config.ConcurrencyConfig) *Manager {
	m := &Manager{
		pools:        map[string]*pool{},
		queueStrat:   defaultStrategy,
		maxWait:      defaultMaxWait,
		maxQueueSize: defaultMaxQueueSize,
	}
	if cfg == nil {
		return m
	}
	if s := string(cfg.Queue.Strategy); s != "" {
		m.queueStrat = s
	}
	if cfg.Queue.MaxWait > 0 {
		m.maxWait = cfg.Queue.MaxWait
	}
	if cfg.Queue.MaxQueueSize > 0 {
		m.maxQueueSize = cfg.Queue.MaxQueueSize
	}
	for name, p := range cfg.Pools {
		max := p.Max
		if max <= 0 {
			max = 1
		}
		m.pools[name] = &pool{name: name, sem: make(chan struct{}, max)}
	}
	return m
}

// Acquire returns a Slot for poolName or an error. It never blocks when a
// slot is free. Under contention the behaviour depends on the manager
// strategy:
//
//   - "reject"  — returns ErrPoolFull immediately.
//   - "wait"    — enqueues the caller up to max_queue_size waiters; above
//     that, returns ErrQueueFull immediately. Otherwise waits until a slot
//     frees, ctx is cancelled (returns ctx.Err()) or max_wait elapses
//     (returns ErrAcquireTimeout).
//
// Unknown pools produce an error wrapping ErrUnknownPool.
func (m *Manager) Acquire(ctx context.Context, poolName string) (*Slot, error) {
	p, ok := m.pools[poolName]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPool, poolName)
	}

	select {
	case p.sem <- struct{}{}:
		p.acquired.Add(1)
		return newSlot(p), nil
	default:
	}

	if m.queueStrat == StrategyReject {
		p.rejected.Add(1)
		return nil, ErrPoolFull
	}

	if int(p.waiting.Load()) >= m.maxQueueSize {
		p.rejected.Add(1)
		return nil, ErrQueueFull
	}

	p.waiting.Add(1)
	p.queued.Add(1)
	defer p.waiting.Add(-1)

	var timeout <-chan time.Time
	if m.maxWait > 0 {
		t := time.NewTimer(m.maxWait)
		defer t.Stop()
		timeout = t.C
	}

	select {
	case p.sem <- struct{}{}:
		p.acquired.Add(1)
		return newSlot(p), nil
	case <-timeout:
		p.timedOut.Add(1)
		return nil, ErrAcquireTimeout
	case <-ctx.Done():
		p.timedOut.Add(1)
		return nil, ctx.Err()
	}
}

// newSlot wraps the release in a sync.Once so duplicate Release calls are
// no-ops. Without this guard a double Release would drain a second slot and
// break the invariant len(sem) <= cap(sem).
func newSlot(p *pool) *Slot {
	var once sync.Once
	return &Slot{release: func() {
		once.Do(func() { <-p.sem })
	}}
}

// Stats returns the current counters and occupancy for poolName.
func (m *Manager) Stats(poolName string) (Stats, error) {
	p, ok := m.pools[poolName]
	if !ok {
		return Stats{}, fmt.Errorf("%w: %q", ErrUnknownPool, poolName)
	}
	return Stats{
		Pool:     p.name,
		Size:     cap(p.sem),
		InUse:    len(p.sem),
		Waiting:  int(p.waiting.Load()),
		Acquired: p.acquired.Load(),
		Rejected: p.rejected.Load(),
		Queued:   p.queued.Load(),
		TimedOut: p.timedOut.Load(),
	}, nil
}

// PoolNames returns the configured pool names in lexicographic order.
func (m *Manager) PoolNames() []string {
	out := make([]string, 0, len(m.pools))
	for k := range m.pools {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
