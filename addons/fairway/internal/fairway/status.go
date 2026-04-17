package fairway

import (
	"os"
	"sync"
	"time"
)

// StatusSnapshot describes the current runtime state exposed by the Fairway daemon.
type StatusSnapshot struct {
	Version     string    `json:"version"`
	Bind        string    `json:"bind"`
	Port        int       `json:"port"`
	RouteCount  int       `json:"routeCount"`
	PID         int       `json:"pid"`
	ConfigPath  string    `json:"configPath,omitempty"`
	SocketPath  string    `json:"socketPath,omitempty"`
	PIDFilePath string    `json:"pidFilePath,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
}

type statusProvider interface {
	Status() StatusSnapshot
}

type runtimeStatus struct {
	mu sync.RWMutex

	version     string
	bind        string
	port        int
	routeCount  int
	pid         int
	configPath  string
	socketPath  string
	pidfilePath string
	startedAt   time.Time
}

func newRuntimeStatus(cfg Config, version, configPath, socketPath, pidfilePath string) *runtimeStatus {
	return &runtimeStatus{
		version:     version,
		bind:        cfg.Bind,
		port:        cfg.Port,
		routeCount:  len(cfg.Routes),
		pid:         os.Getpid(),
		configPath:  configPath,
		socketPath:  socketPath,
		pidfilePath: pidfilePath,
	}
}

func (s *runtimeStatus) MarkStarted(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startedAt = now.UTC()
}

func (s *runtimeStatus) MarkStopped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startedAt = time.Time{}
}

func (s *runtimeStatus) Status() StatusSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return StatusSnapshot{
		Version:     s.version,
		Bind:        s.bind,
		Port:        s.port,
		RouteCount:  s.routeCount,
		PID:         s.pid,
		ConfigPath:  s.configPath,
		SocketPath:  s.socketPath,
		PIDFilePath: s.pidfilePath,
		StartedAt:   s.startedAt,
	}
}
