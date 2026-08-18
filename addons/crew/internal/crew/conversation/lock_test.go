package conversation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

func lockAgent(dir string, wait time.Duration) *crew.Agent {
	a := cliAgent(dir)
	a.Conversation.LockWait = wait
	return a
}

// The second acquire must wait rather than proceed: two runs resuming the
// same external session concurrently would fork it.
func TestLockSerialisesSameKey(t *testing.T) {
	dir := t.TempDir()
	s := NewStateful(nil)
	a := lockAgent(dir, 150*time.Millisecond)

	release, err := s.Lock(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	if _, err := s.Lock(context.Background(), a, "chat-1"); err == nil {
		t.Fatalf("second lock should have timed out while the first is held")
	} else if !strings.Contains(err.Error(), "busy") {
		t.Fatalf("want busy error, got %v", err)
	}

	release()

	release2, err := s.Lock(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
}

func TestLockAllowsDifferentKeys(t *testing.T) {
	dir := t.TempDir()
	s := NewStateful(nil)
	a := lockAgent(dir, 100*time.Millisecond)

	r1, err := s.Lock(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("lock chat-1: %v", err)
	}
	defer r1()

	r2, err := s.Lock(context.Background(), a, "chat-2")
	if err != nil {
		t.Fatalf("lock chat-2 should not block on chat-1: %v", err)
	}
	r2()
}

// A waiter hands the lock over as soon as the holder releases it, instead of
// failing outright — the whole point of "second run waits".
func TestLockWaiterProceedsAfterRelease(t *testing.T) {
	dir := t.TempDir()
	s := NewStateful(nil)
	a := lockAgent(dir, 5*time.Second)

	release, err := s.Lock(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	acquired := make(chan error, 1)
	go func() {
		r, err := s.Lock(context.Background(), a, "chat-1")
		if r != nil {
			r()
		}
		acquired <- err
	}()

	// Give the waiter time to observe the held lock and start polling.
	time.Sleep(120 * time.Millisecond)
	select {
	case err := <-acquired:
		t.Fatalf("waiter acquired the lock while it was held: %v", err)
	default:
	}

	release()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("waiter failed after release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("waiter never acquired the lock after release")
	}
}

func TestLockHonoursContextCancellation(t *testing.T) {
	dir := t.TempDir()
	s := NewStateful(nil)
	a := lockAgent(dir, time.Hour)

	release, err := s.Lock(context.Background(), a, "chat-1")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, err := s.Lock(ctx, a, "chat-1"); err == nil {
		t.Fatalf("want cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("lock ignored ctx cancellation for %s", elapsed)
	}
}

func TestStatelessLockIsNoop(t *testing.T) {
	release, err := NewStateless().Lock(context.Background(), &crew.Agent{}, "")
	if err != nil {
		t.Fatalf("stateless lock: %v", err)
	}
	release()
}
