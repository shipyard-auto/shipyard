package fairway_test

import (
	"sync"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

func TestObserve_incrementsTotal(t *testing.T) {
	t.Parallel()

	s := fairway.NewStats(time.Now())
	s.Observe("/foo", 200, 0)
	s.Observe("/bar", 201, 0)

	snap := s.Snapshot()
	if snap.Total != 2 {
		t.Errorf("Total = %d; want 2", snap.Total)
	}
}

func TestObserve_incrementsPerRoute(t *testing.T) {
	t.Parallel()

	s := fairway.NewStats(time.Now())
	s.Observe("/foo", 200, 0)
	s.Observe("/foo", 200, 0)
	s.Observe("/bar", 200, 0)

	snap := s.Snapshot()
	if snap.ByRoute["/foo"].Count != 2 {
		t.Errorf("ByRoute[/foo].Count = %d; want 2", snap.ByRoute["/foo"].Count)
	}
	if snap.ByRoute["/bar"].Count != 1 {
		t.Errorf("ByRoute[/bar].Count = %d; want 1", snap.ByRoute["/bar"].Count)
	}
}

func TestObserve_incrementsPerStatus(t *testing.T) {
	t.Parallel()

	s := fairway.NewStats(time.Now())
	s.Observe("/foo", 200, 0)
	s.Observe("/foo", 200, 0)
	s.Observe("/foo", 404, 0)

	snap := s.Snapshot()
	if snap.ByStatus[200] != 2 {
		t.Errorf("ByStatus[200] = %d; want 2", snap.ByStatus[200])
	}
	if snap.ByStatus[404] != 1 {
		t.Errorf("ByStatus[404] = %d; want 1", snap.ByStatus[404])
	}
}

func TestObserveResult_incrementsPerExitCode(t *testing.T) {
	t.Parallel()

	s := fairway.NewStats(time.Now())
	s.ObserveResult("/foo", 200, 0, 0)
	s.ObserveResult("/foo", 500, 1, 0)
	s.ObserveResult("/foo", 502, 42, 0)

	snap := s.Snapshot()
	if snap.ByExitCode[0] != 1 {
		t.Errorf("ByExitCode[0] = %d; want 1", snap.ByExitCode[0])
	}
	if snap.ByExitCode[1] != 1 {
		t.Errorf("ByExitCode[1] = %d; want 1", snap.ByExitCode[1])
	}
	if snap.ByExitCode[42] != 1 {
		t.Errorf("ByExitCode[42] = %d; want 1", snap.ByExitCode[42])
	}
}

func TestObserve_errorStatusIncrementsErrCount(t *testing.T) {
	t.Parallel()

	s := fairway.NewStats(time.Now())
	s.Observe("/foo", 200, 0) // not an error
	s.Observe("/foo", 500, 0) // error
	s.Observe("/foo", 503, 0) // error

	snap := s.Snapshot()
	if snap.ByRoute["/foo"].ErrCount != 2 {
		t.Errorf("ErrCount = %d; want 2", snap.ByRoute["/foo"].ErrCount)
	}
}

func TestSnapshot_returnsDeepCopy(t *testing.T) {
	t.Parallel()

	s := fairway.NewStats(time.Now())
	s.Observe("/foo", 200, 0)

	snap1 := s.Snapshot()
	s.Observe("/foo", 200, 0) // mutate after snapshot

	snap2 := s.Snapshot()

	if snap1.Total == snap2.Total {
		// snap1 should have 1, snap2 should have 2
		t.Errorf("snap1.Total = snap2.Total = %d; expected different values", snap1.Total)
	}
	if snap1.Total != 1 {
		t.Errorf("snap1.Total = %d; want 1", snap1.Total)
	}
	if snap2.Total != 2 {
		t.Errorf("snap2.Total = %d; want 2", snap2.Total)
	}

	// Mutate the snapshot map — should not affect the Stats
	snap1.ByRoute["/injected"] = fairway.RouteStats{Count: 999}
	snap3 := s.Snapshot()
	if _, ok := snap3.ByRoute["/injected"]; ok {
		t.Error("mutating snapshot map leaked into Stats")
	}
}

func TestObserve_raceFreeUnderStress(t *testing.T) {
	t.Parallel()

	s := fairway.NewStats(time.Now())

	const goroutines = 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			s.Observe("/stress", 200, 0)
		}()
	}
	wg.Wait()

	snap := s.Snapshot()
	if snap.Total != goroutines {
		t.Errorf("Total = %d; want %d", snap.Total, goroutines)
	}
	if snap.ByRoute["/stress"].Count != goroutines {
		t.Errorf("ByRoute[/stress].Count = %d; want %d", snap.ByRoute["/stress"].Count, goroutines)
	}
}
