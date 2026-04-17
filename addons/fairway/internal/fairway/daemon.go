package fairway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

const defaultShutdownTimeout = 5 * time.Second

// BootstrapConfig wires the concrete Fairway daemon dependencies from persisted config.
type BootstrapConfig struct {
	ConfigPath      string
	SocketPath      string
	PIDFilePath     string
	ShipyardBinary  string
	Logger          EventLogger
	Version         string
	ShutdownTimeout time.Duration
}

type httpDaemon interface {
	Start() error
	Shutdown(context.Context) error
	Errors() <-chan error
}

type socketDaemon interface {
	Start(path string) error
	Shutdown() error
	Errors() <-chan error
}

type pidfileLock interface {
	Acquire() error
	Release() error
	Path() string
}

// Daemon manages the Fairway HTTP and control-plane servers as a single runtime unit.
type Daemon struct {
	http    httpDaemon
	socket  socketDaemon
	pidfile pidfileLock
	status  *runtimeStatus
	logger  EventLogger

	socketPath      string
	shutdownTimeout time.Duration

	started bool
}

// NewDaemon constructs a runnable Fairway daemon from on-disk configuration and defaults.
func NewDaemon(cfg BootstrapConfig) (*Daemon, error) {
	configPath := cfg.ConfigPath
	if configPath == "" {
		var err error
		configPath, err = DefaultConfigPath()
		if err != nil {
			return nil, err
		}
	}

	socketPath := cfg.SocketPath
	if socketPath == "" {
		var err error
		socketPath, err = DefaultSocketPath()
		if err != nil {
			return nil, err
		}
	}

	pidfilePath := cfg.PIDFilePath
	if pidfilePath == "" {
		var err error
		pidfilePath, err = DefaultPIDFilePath()
		if err != nil {
			return nil, err
		}
	}

	repo := NewFileRepositoryAt(configPath)
	router, err := NewRouter(repo)
	if err != nil {
		return nil, err
	}

	runtimeConfig := router.Config()
	status := newRuntimeStatus(runtimeConfig, cfg.Version, configPath, socketPath, pidfilePath)
	logger := cfg.Logger
	if logger == nil {
		var err error
		logger, err = NewLogger()
		if err != nil {
			return nil, err
		}
	}
	executor := NewExecutor(ExecutorConfig{
		ShipyardBinary: cfg.ShipyardBinary,
		MaxInFlight:    runtimeConfig.MaxInFlight,
	})

	httpServer, err := NewServer(ServerConfig{
		Router:   router,
		Executor: executor,
		Logger:   logger,
	})
	if err != nil {
		return nil, err
	}

	socketServer, err := NewSocketServer(SocketServerConfig{
		Router:  router,
		Version: cfg.Version,
		Status:  status,
	})
	if err != nil {
		return nil, err
	}

	daemon, err := newDaemon(httpServer, socketServer, NewPIDFile(pidfilePath), socketPath, cfg.ShutdownTimeout)
	if err != nil {
		return nil, err
	}
	daemon.status = status
	daemon.logger = logger
	return daemon, nil
}

func newDaemon(httpServer httpDaemon, socketServer socketDaemon, pidfile pidfileLock, socketPath string, shutdownTimeout time.Duration) (*Daemon, error) {
	if httpServer == nil {
		return nil, errors.New("http server is required")
	}
	if socketServer == nil {
		return nil, errors.New("socket server is required")
	}
	if pidfile == nil {
		return nil, errors.New("pidfile is required")
	}
	if socketPath == "" {
		return nil, errors.New("socket path is required")
	}
	if shutdownTimeout <= 0 {
		shutdownTimeout = defaultShutdownTimeout
	}
	return &Daemon{
		http:            httpServer,
		socket:          socketServer,
		pidfile:         pidfile,
		socketPath:      socketPath,
		shutdownTimeout: shutdownTimeout,
	}, nil
}

// Run starts the daemon, blocks until shutdown, and tears down runtime state cleanly.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.start(); err != nil {
		return err
	}
	defer func() {
		_ = d.shutdown()
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-d.http.Errors():
		if err == nil || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case err := <-d.socket.Errors():
		if err == nil || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}

func (d *Daemon) start() error {
	if err := d.pidfile.Acquire(); err != nil {
		return err
	}

	if err := prepareSocketPath(d.socketPath); err != nil {
		_ = d.pidfile.Release()
		return err
	}

	if err := d.socket.Start(d.socketPath); err != nil {
		_ = removeSocketPath(d.socketPath)
		_ = d.pidfile.Release()
		return err
	}

	if err := d.http.Start(); err != nil {
		_ = d.socket.Shutdown()
		_ = removeSocketPath(d.socketPath)
		_ = d.pidfile.Release()
		return err
	}

	d.started = true
	if d.status != nil {
		d.status.MarkStarted(time.Now())
	}
	d.logEvent("info", "fairway_daemon_started", "Fairway daemon started", nil)
	return nil
}

func (d *Daemon) shutdown() error {
	if !d.started {
		return nil
	}
	d.started = false
	if d.status != nil {
		d.status.MarkStopped()
	}
	d.logEvent("info", "fairway_daemon_stopped", "Fairway daemon stopped", nil)

	ctx, cancel := context.WithTimeout(context.Background(), d.shutdownTimeout)
	defer cancel()

	var shutdownErr error
	if err := d.http.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
		shutdownErr = errors.Join(shutdownErr, err)
	}
	if err := d.socket.Shutdown(); err != nil && !errors.Is(err, net.ErrClosed) {
		shutdownErr = errors.Join(shutdownErr, err)
	}
	if err := removeSocketPath(d.socketPath); err != nil {
		shutdownErr = errors.Join(shutdownErr, err)
	}
	if err := d.pidfile.Release(); err != nil {
		shutdownErr = errors.Join(shutdownErr, err)
	}
	return shutdownErr
}

// Status returns the last known daemon runtime snapshot.
func (d *Daemon) Status() StatusSnapshot {
	if d.status == nil {
		return StatusSnapshot{}
	}
	return d.status.Status()
}

func (d *Daemon) logEvent(level, eventName, message string, data map[string]any) {
	if d.logger == nil {
		return
	}

	snapshot := d.Status()
	if data == nil {
		data = map[string]any{}
	}
	if snapshot.SocketPath != "" {
		data["socketPath"] = snapshot.SocketPath
	}
	if snapshot.PIDFilePath != "" {
		data["pidFilePath"] = snapshot.PIDFilePath
	}
	if snapshot.ConfigPath != "" {
		data["configPath"] = snapshot.ConfigPath
	}
	if snapshot.Port != 0 {
		data["port"] = snapshot.Port
	}
	if snapshot.Bind != "" {
		data["bind"] = snapshot.Bind
	}

	_ = d.logger.Write(yardlogs.Event{
		Source:     fairwayLogSource,
		Level:      level,
		Event:      eventName,
		Message:    message,
		EntityType: "daemon",
		EntityID:   "fairway",
		EntityName: "fairway",
		Data:       data,
	})
}

func prepareSocketPath(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create socket dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod socket dir %s: %w", dir, err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat socket path %s: %w", path, err)
	}

	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("socket path %s exists and is not a socket", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	return nil
}

func removeSocketPath(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove socket path %s: %w", path, err)
	}
	return nil
}
