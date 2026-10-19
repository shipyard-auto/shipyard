package wa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// Exit codes of `serve`. Distinct codes let the service manager's logs
// say why the daemon stopped without parsing text.
const (
	ExitOK             = 0
	ExitAlreadyRunning = 10
	ExitNotPaired      = 20
	ExitRuntime        = 30
	ExitLoggedOut      = 40
)

// ExitError carries a serve exit code.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

var errNoDevice = errors.New("no paired device: run 'shipyard-comms-whatsapp pair' first")

// PruneInterval is how often the daemon applies archive TTLs.
const PruneInterval = time.Hour

// ServeOptions configures Serve.
type ServeOptions struct {
	Paths  Paths
	Config Config
	Log    *slog.Logger
}

// Serve runs the daemon until ctx is cancelled or the session ends. It
// returns nil on a clean shutdown and *ExitError otherwise.
func Serve(ctx context.Context, opts ServeOptions) error {
	log := opts.Log
	lock, err := AcquireLock(opts.Paths)
	if err != nil {
		if errors.Is(err, ErrSessionBusy) {
			return &ExitError{Code: ExitAlreadyRunning, Err: err}
		}
		return &ExitError{Code: ExitRuntime, Err: err}
	}
	defer lock.Release()
	if _, err := os.Stat(opts.Paths.Session); errors.Is(err, os.ErrNotExist) {
		return &ExitError{Code: ExitNotPaired, Err: errNoDevice}
	}

	arch, err := archive.Open(ctx, opts.Paths.Archive)
	if err != nil {
		return &ExitError{Code: ExitRuntime, Err: err}
	}
	defer func() { _ = arch.Close() }()

	sess, err := OpenSession(ctx, opts.Paths, opts.Config.DeviceName, SlogLogger{L: log})
	if err != nil {
		return &ExitError{Code: ExitRuntime, Err: err}
	}
	defer sess.Close()
	if !sess.Paired() {
		return &ExitError{Code: ExitNotPaired, Err: errNoDevice}
	}

	tracker := NewTracker(opts.Paths.Status, os.Getpid(), nil)
	finalState, finalDetail := StateStopped, ""
	defer func() { tracker.SetState(finalState, finalDetail) }()

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// Config edits made by the CLI (routes, pause, limits) apply live.
	cfgCache := NewConfigCache(opts.Paths.Config)
	deliverer := NewDeliverer(func() string {
		cfg, _ := cfgCache.Get()
		return FairwayAddr(cfg.FairwayAddr, opts.Paths.FairwayDir)
	}, log)
	go deliverer.Run(ctx)
	// Downloads queued before the last stop died with that process.
	if _, err := arch.FailPendingMedia(ctx, "interrupted by a daemon restart"); err != nil {
		log.Error(yardlogs.EventWAArchiveError, slog.String(yardlogs.KeyMessage, "cannot reset pending media"), slog.String(yardlogs.KeyError, err.Error()))
	}
	downloader := NewDownloader(arch, sess.Fetcher(), log)
	go downloader.Run(ctx)
	h := &Handler{
		Archive:  arch,
		Dir:      sess.Directory(),
		Tracker:  tracker,
		Log:      log,
		OnFatal:  func(err error) { cancel(err) },
		Routes:   func() Routes { cfg, _ := cfgCache.Get(); return cfg.Routes },
		Deliver:  deliverer.Enqueue,
		Events:   func() bool { cfg, _ := cfgCache.Get(); return cfg.PublishEvents },
		Publish:  deliverer.Publish,
		Media:    func() MediaConfig { cfg, _ := cfgCache.Get(); return cfg.Media },
		Download: downloader.Enqueue,
	}
	sess.Client.AddEventHandler(func(evt any) { h.Handle(ctx, evt) })

	log.Info(yardlogs.EventWADaemonStarted, slog.String(yardlogs.KeyMessage, "whatsapp daemon starting"))
	if err := sess.Client.Connect(); err != nil {
		return &ExitError{Code: ExitRuntime, Err: fmt.Errorf("connect: %w", err)}
	}

	// The control socket is the only way to send: every message passes
	// through the Outbox policy, whoever asks.
	ln, err := ListenRPC(opts.Paths.Socket)
	if err != nil {
		return &ExitError{Code: ExitRuntime, Err: err}
	}
	defer func() { _ = os.Remove(opts.Paths.Socket) }()
	outbox := &Outbox{
		Archive: arch,
		Sender:  sess.Sender(),
		Config:  cfgCache.Get,
		Self:    func() types.JID { pn, _ := sess.Directory().Own(); return pn },
		Log:     log,
		Now:     time.Now,
		Sleep:   SleepCtx,
	}
	rpc := &RPCServer{Outbox: outbox, Tracker: tracker, Log: log}
	var uploads *UploadStore
	// The query API is optional; failing to bind it is logged, not fatal:
	// receiving and sending keep working.
	if cfg, _ := cfgCache.Get(); cfg.APIAddr != "" {
		// Without an upload area the API still serves everything else.
		if uploads, err = NewUploadStore(opts.Paths.Uploads); err != nil {
			log.Error(yardlogs.EventWAAPIError, slog.String(yardlogs.KeyMessage, "uploads disabled"), slog.String(yardlogs.KeyError, err.Error()))
		}
		api := &APIServer{
			Archive: arch,
			Token:   func() (string, error) { return ReadAPIToken(opts.Paths.APIToken) },
			Self:    func() types.JID { pn, _ := sess.Directory().Own(); return pn },
			Log:     log,
			Outbox:  outbox,
			Uploads: uploads,
		}
		if stop, err := ServeAPI(ctx, cfg.APIAddr, api.Handler()); err != nil {
			log.Error(yardlogs.EventWAAPIError, slog.String(yardlogs.KeyMessage, "query API not started"), slog.String(yardlogs.KeyError, err.Error()))
		} else {
			tracker.SetAPI(cfg.APIAddr)
			defer stop()
		}
	}
	rpcDone := make(chan struct{})
	go func() {
		defer close(rpcDone)
		rpc.Serve(ctx, ln)
	}()
	defer func() {
		cancel(nil)
		<-rpcDone
	}()

	prune := func() {
		if uploads != nil {
			uploads.Sweep()
		}
		n, err := arch.Prune(ctx, opts.Config.DefaultTTLDuration())
		if err != nil {
			h.archiveError("", err)
			return
		}
		if n > 0 {
			log.Info(yardlogs.EventWAPruned, slog.String(yardlogs.KeyMessage, "expired messages pruned"), slog.Int64(yardlogs.KeyCount, n))
		}
		// Files left behind by a crash between a delete and its file
		// removal, or by an interrupted download.
		if n, err := arch.SweepMedia(ctx, MediaSweepGrace); err != nil {
			h.archiveError("", err)
		} else if n > 0 {
			log.Info(yardlogs.EventWAPruned, slog.String(yardlogs.KeyMessage, "orphan media files removed"), slog.Int(yardlogs.KeyCount, n))
		}
	}
	prune()

	heartbeat := time.NewTicker(HeartbeatInterval)
	defer heartbeat.Stop()
	pruneTick := time.NewTicker(PruneInterval)
	defer pruneTick.Stop()
	var lastWriteErr string
	for {
		select {
		case <-ctx.Done():
			cause := context.Cause(ctx)
			log.Info(yardlogs.EventWADaemonStopped, slog.String(yardlogs.KeyMessage, "whatsapp daemon stopping"))
			switch {
			case errors.Is(cause, ErrLoggedOut):
				finalState, finalDetail = StateLoggedOut, cause.Error()
				return &ExitError{Code: ExitLoggedOut, Err: cause}
			case errors.Is(cause, ErrStreamReplaced):
				finalState, finalDetail = StateStreamReplace, cause.Error()
				return &ExitError{Code: ExitRuntime, Err: cause}
			}
			return nil
		case <-heartbeat.C:
			tracker.SetDeliveries(deliverer.Counts())
			// Log a status-file failure once, not every 30s.
			msg := ""
			if err := tracker.Heartbeat(); err != nil {
				msg = err.Error()
			}
			if msg != "" && msg != lastWriteErr {
				log.Error(yardlogs.EventWAStatusError, slog.String(yardlogs.KeyMessage, "cannot write status file"), slog.String(yardlogs.KeyError, msg))
			}
			lastWriteErr = msg
		case <-pruneTick.C:
			prune()
		}
	}
}
