package conversation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// lockPollInterval is how often a waiter retries a non-blocking flock. flock
// itself offers no timeout, and a blocking acquire could not honour ctx or
// Conversation.LockWait — polling keeps both, at a cost of at most one
// interval of extra latency per handoff.
const lockPollInterval = 50 * time.Millisecond

// Lock serialises runs that share a conversation key. The lock is an
// exclusive flock on <agent.Dir>/locks/<hash16(key)>.lock, held across the
// whole load-run-save cycle so two concurrent runs cannot resume the same
// external session and fork it.
//
// It is inter-process on purpose: the same agent can be driven by the daemon
// and by a `shipyard crew run` subprocess at the same time, so an in-process
// mutex would not see both.
//
// The returned release is always safe to call, including after an error path
// elsewhere in the caller. Waiting is bounded by agent.Conversation.LockWait
// (DefaultLockWait when unset) and by ctx, whichever fires first.
func (s *Stateful) Lock(ctx context.Context, agent *crew.Agent, key string) (func(), error) {
	if agent == nil {
		return nil, errors.New("conversation: agent is required")
	}
	dir := filepath.Join(agent.Dir, locksDir)
	if err := os.MkdirAll(dir, sessionDirMode); err != nil {
		return nil, fmt.Errorf("mkdir locks dir: %w", err)
	}
	path := filepath.Join(dir, hashedKey(key)+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, sessionFileMode)
	if err != nil {
		return nil, fmt.Errorf("open conversation lock: %w", err)
	}

	wait := agent.Conversation.LockWait
	if wait <= 0 {
		wait = crew.DefaultLockWait
	}
	deadline := s.now().Add(wait)

	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				// Closing the descriptor releases the flock; doing both
				// keeps the intent explicit if the file is ever reused.
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lock conversation %q: %w", key, err)
		}
		if !s.now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("conversation %q is busy: another run held it for longer than %s", key, wait)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("lock conversation %q: %w", key, ctx.Err())
		case <-time.After(lockPollInterval):
		}
	}
}
