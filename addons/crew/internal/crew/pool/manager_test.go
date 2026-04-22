package pool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/config"
)

func cfg(max int, strategy config.QueueStrategy, wait time.Duration, qmax int) *config.ConcurrencyConfig {
	return &config.ConcurrencyConfig{
		DefaultPool: "cli",
		Pools:       map[string]config.PoolConfig{"cli": {Max: max}},
		Queue:       config.QueueConfig{Strategy: strategy, MaxWait: wait, MaxQueueSize: qmax},
	}
}

func TestAcquireWithinCapacity(t *testing.T) {
	m := NewManager(cfg(2, config.QueueWait, 100*time.Millisecond, 8))
	ctx := context.Background()

	s1, err := m.Acquire(ctx, "cli")
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	s2, err := m.Acquire(ctx, "cli")
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}

	st, err := m.Stats("cli")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.InUse != 2 || st.Size != 2 {
		t.Fatalf("want InUse=2 Size=2, got InUse=%d Size=%d", st.InUse, st.Size)
	}
	if st.Acquired != 2 {
		t.Fatalf("want Acquired=2, got %d", st.Acquired)
	}

	s1.Release()
	s2.Release()
}

func TestAcquireBlocksUntilRelease(t *testing.T) {
	m := NewManager(cfg(1, config.QueueWait, 500*time.Millisecond, 8))
	ctx := context.Background()

	s1, err := m.Acquire(ctx, "cli")
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}

	got := make(chan error, 1)
	start := time.Now()
	go func() {
		s2, err := m.Acquire(ctx, "cli")
		if err == nil {
			s2.Release()
		}
		got <- err
	}()

	time.Sleep(20 * time.Millisecond)
	s1.Release()

	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("waiter: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Fatalf("waiter took too long: %s", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waiter did not complete")
	}
}

func TestRejectWhenFull(t *testing.T) {
	m := NewManager(cfg(1, config.QueueReject, 100*time.Millisecond, 8))
	ctx := context.Background()

	s1, err := m.Acquire(ctx, "cli")
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	defer s1.Release()

	start := time.Now()
	s2, err := m.Acquire(ctx, "cli")
	if err == nil {
		s2.Release()
		t.Fatal("expected ErrPoolFull, got nil")
	}
	if !errors.Is(err, ErrPoolFull) {
		t.Fatalf("want ErrPoolFull, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("reject should be immediate, took %s", elapsed)
	}

	st, _ := m.Stats("cli")
	if st.Rejected != 1 {
		t.Fatalf("want Rejected=1, got %d", st.Rejected)
	}
}

func TestTimeoutViaMaxWait(t *testing.T) {
	m := NewManager(cfg(1, config.QueueWait, 20*time.Millisecond, 8))
	ctx := context.Background()

	s1, err := m.Acquire(ctx, "cli")
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	defer s1.Release()

	start := time.Now()
	_, err = m.Acquire(ctx, "cli")
	if !errors.Is(err, ErrAcquireTimeout) {
		t.Fatalf("want ErrAcquireTimeout, got %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 15*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Fatalf("unexpected elapsed: %s", elapsed)
	}

	st, _ := m.Stats("cli")
	if st.TimedOut != 1 {
		t.Fatalf("want TimedOut=1, got %d", st.TimedOut)
	}
}

func TestCtxCancelDuringWait(t *testing.T) {
	m := NewManager(cfg(1, config.QueueWait, time.Second, 8))
	base := context.Background()

	s1, err := m.Acquire(base, "cli")
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	defer s1.Release()

	ctx, cancel := context.WithCancel(base)
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err = m.Acquire(ctx, "cli")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestQueueFull(t *testing.T) {
	m := NewManager(cfg(1, config.QueueWait, time.Second, 2))
	base := context.Background()

	s1, err := m.Acquire(base, "cli")
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	defer s1.Release()

	// Launch two waiters that will stay blocked while the holder keeps the slot.
	ctx, cancel := context.WithCancel(base)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_, _ = m.Acquire(ctx, "cli")
		}()
	}

	// Wait until both waiters are accounted for.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		st, _ := m.Stats("cli")
		if st.Waiting == 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	st, _ := m.Stats("cli")
	if st.Waiting != 2 {
		t.Fatalf("want Waiting=2, got %d", st.Waiting)
	}

	// Fourth request: slot held, queue full → immediate ErrQueueFull.
	start := time.Now()
	_, err = m.Acquire(base, "cli")
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("queue full reject should be immediate, took %s", elapsed)
	}

	cancel()
	wg.Wait()
}

func TestUnknownPool(t *testing.T) {
	m := NewManager(cfg(1, config.QueueWait, 10*time.Millisecond, 8))
	_, err := m.Acquire(context.Background(), "xyz")
	if !errors.Is(err, ErrUnknownPool) {
		t.Fatalf("want ErrUnknownPool, got %v", err)
	}
	if _, err := m.Stats("xyz"); !errors.Is(err, ErrUnknownPool) {
		t.Fatalf("Stats: want ErrUnknownPool, got %v", err)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	m := NewManager(cfg(1, config.QueueWait, 10*time.Millisecond, 8))
	ctx := context.Background()

	s1, err := m.Acquire(ctx, "cli")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	s1.Release()
	s1.Release() // must not panic or free an extra slot

	st, _ := m.Stats("cli")
	if st.InUse != 0 {
		t.Fatalf("want InUse=0, got %d", st.InUse)
	}

	// Reacquire must still work after duplicate Release.
	s2, err := m.Acquire(ctx, "cli")
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	s2.Release()

	// A nil-pointer Release must also be safe.
	var nilSlot *Slot
	nilSlot.Release()
}

func TestStatsCounters(t *testing.T) {
	m := NewManager(cfg(1, config.QueueReject, 10*time.Millisecond, 2))
	ctx := context.Background()

	s1, err := m.Acquire(ctx, "cli")
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if _, err := m.Acquire(ctx, "cli"); !errors.Is(err, ErrPoolFull) {
		t.Fatalf("want ErrPoolFull, got %v", err)
	}
	s1.Release()

	st, _ := m.Stats("cli")
	if st.Acquired != 1 || st.Rejected != 1 {
		t.Fatalf("counters: %+v", st)
	}
}

func TestPoolsAreIsolated(t *testing.T) {
	cfg := &config.ConcurrencyConfig{
		DefaultPool: "cli",
		Pools: map[string]config.PoolConfig{
			"cli":    {Max: 1},
			"ollama": {Max: 1},
		},
		Queue: config.QueueConfig{Strategy: config.QueueReject, MaxWait: time.Second, MaxQueueSize: 8},
	}
	m := NewManager(cfg)
	ctx := context.Background()

	sCli, err := m.Acquire(ctx, "cli")
	if err != nil {
		t.Fatalf("acquire cli: %v", err)
	}
	defer sCli.Release()

	sOl, err := m.Acquire(ctx, "ollama")
	if err != nil {
		t.Fatalf("acquire ollama: %v", err)
	}
	defer sOl.Release()

	names := m.PoolNames()
	if len(names) != 2 || names[0] != "cli" || names[1] != "ollama" {
		t.Fatalf("PoolNames: %v", names)
	}
}

func TestConcurrencyRespectsMaxUnderLoad(t *testing.T) {
	const max = 4
	m := NewManager(cfg(max, config.QueueWait, 2*time.Second, 64))
	ctx := context.Background()

	var inUse atomic.Int64
	var peak atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := m.Acquire(ctx, "cli")
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			cur := inUse.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			inUse.Add(-1)
			s.Release()
		}()
	}
	wg.Wait()

	if got := peak.Load(); got > max {
		t.Fatalf("peak InUse=%d exceeded max=%d", got, max)
	}
	if got := peak.Load(); got < 2 {
		t.Fatalf("peak InUse=%d suspiciously low — semaphore not working?", got)
	}
}

func TestNewManagerNilConfig(t *testing.T) {
	m := NewManager(nil)
	if len(m.PoolNames()) != 0 {
		t.Fatalf("want no pools, got %v", m.PoolNames())
	}
	if _, err := m.Acquire(context.Background(), "any"); !errors.Is(err, ErrUnknownPool) {
		t.Fatalf("want ErrUnknownPool, got %v", err)
	}
}

func TestNewManagerNormalisesPoolMax(t *testing.T) {
	cfg := &config.ConcurrencyConfig{
		Pools: map[string]config.PoolConfig{"weird": {Max: 0}},
		Queue: config.QueueConfig{Strategy: config.QueueReject, MaxWait: time.Millisecond, MaxQueueSize: 1},
	}
	m := NewManager(cfg)
	st, err := m.Stats("weird")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Size != 1 {
		t.Fatalf("want Size=1 (normalised), got %d", st.Size)
	}
}
