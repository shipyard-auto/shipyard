package wa

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrSessionBusy is returned by AcquireLock when another process (normally
// the daemon) already owns the session.
var ErrSessionBusy = errors.New("whatsapp session is in use by another process (is the daemon running? see 'shipyard-comms-whatsapp status')")

// Lock is an exclusive advisory lock on run/wa.lock. Only one process may
// hold the linked-device session at a time: two connections with the same
// keys make WhatsApp drop one of them ("stream replaced").
type Lock struct{ f *os.File }

// AcquireLock takes the lock without blocking.
func AcquireLock(p Paths) (*Lock, error) {
	if err := p.Ensure(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrSessionBusy
		}
		return nil, fmt.Errorf("whatsapp: lock: %w", err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. Safe to call on nil.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
