package wa

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// State is the daemon's connection state as reported in status.json.
type State string

const (
	StateConnecting    State = "connecting"
	StateConnected     State = "connected"
	StateDisconnected  State = "disconnected"  // network drop; whatsmeow reconnects on its own
	StateLoggedOut     State = "logged_out"    // the phone removed this device; pair again
	StateTemporaryBan  State = "temporary_ban" // WhatsApp suspended the account for a while
	StateStreamReplace State = "stream_replaced"
	StateStopped       State = "stopped"
)

// Status is run/status.json. The daemon rewrites it on every state change
// and on a heartbeat; `status` reads it without talking to the daemon.
type Status struct {
	PID           int       `json:"pid"`
	State         State     `json:"state"`
	Since         time.Time `json:"since"`
	HeartbeatAt   time.Time `json:"heartbeat_at"`
	Account       string    `json:"account,omitempty"`        // own phone number, digits only
	PushName      string    `json:"push_name,omitempty"`      // own display name
	LastMessageAt time.Time `json:"last_message_at,omitzero"` // last message seen, any chat
	Detail        string    `json:"detail,omitempty"`         // e.g. ban expiry, logout reason
	// Inbound deliveries to fairway since the daemon started.
	Delivered      int64 `json:"delivered,omitempty"`
	DeliveryFailed int64 `json:"delivery_failed,omitempty"`
	// APIAddr is where the query API is listening; empty when off.
	APIAddr string `json:"api_addr,omitempty"`
}

// HeartbeatInterval is how often the daemon refreshes status.json while
// nothing else changes. Status treats twice this age as stale.
const HeartbeatInterval = 30 * time.Second

// WriteStatus writes s atomically with mode 0600.
func WriteStatus(path string, s Status) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("status: marshal: %w", err)
	}
	return writeFileAtomic(path, append(raw, '\n'))
}

// ReadStatus reads path. A missing file returns (Status{}, false, nil).
func ReadStatus(path string) (Status, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, fmt.Errorf("status: read %s: %w", path, err)
	}
	var s Status
	if err := json.Unmarshal(raw, &s); err != nil {
		return Status{}, false, fmt.Errorf("status: parse %s: %w", path, err)
	}
	return s, true, nil
}

// Running reports whether the daemon that wrote s is still alive: its PID
// exists and the heartbeat is recent. A crash leaves a stale file behind,
// which this treats as not running.
func (s Status) Running(now time.Time) bool {
	if s.PID <= 0 || s.State == StateStopped {
		return false
	}
	if now.Sub(s.HeartbeatAt) > 2*HeartbeatInterval {
		return false
	}
	return processAlive(s.PID)
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
