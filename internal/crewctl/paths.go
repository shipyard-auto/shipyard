package crewctl

import (
	"os"
	"path/filepath"
	"time"
)

// JSON-RPC 2.0 constants shared between the crew daemon and its CLI client.
const (
	JSONRPCVersion = "2.0"

	ErrCodeParseError      = -32700
	ErrCodeInvalidRequest  = -32600
	ErrCodeMethodNotFound  = -32601
	ErrCodeInvalidParams   = -32602
	ErrCodeInternal        = -32603
	ErrCodeVersionMismatch = -32010
	ErrCodeAppSpecific     = -32000

	DefaultHandshakeTimeout = 2 * time.Second
)

// ShipyardHome resolves the shipyard root directory. Honors SHIPYARD_HOME,
// otherwise falls back to "$HOME/.shipyard".
func ShipyardHome() (string, error) {
	if h := os.Getenv("SHIPYARD_HOME"); h != "" {
		return h, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".shipyard"), nil
}

// AgentSocketPath returns the Unix socket path for a given crew agent under
// the supplied shipyard home.
func AgentSocketPath(home, agent string) string {
	return filepath.Join(home, "run", "crew", agent+".sock")
}

// AgentPIDPath returns the PID file path for a given crew agent under the
// supplied shipyard home.
func AgentPIDPath(home, agent string) string {
	return filepath.Join(home, "run", "crew", agent+".pid")
}

// CrewLogsDir returns the directory that holds crew JSONL log files.
func CrewLogsDir(home string) string {
	return filepath.Join(home, "logs", "crew")
}
