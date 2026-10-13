package wa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// LogoutOptions configures Logout.
type LogoutOptions struct {
	Paths  Paths
	Config Config
	Log    *slog.Logger
	// Local skips telling WhatsApp and only deletes the local keys. Use
	// when offline; the device must then be removed on the phone.
	Local bool
}

// ErrNotPaired is returned when there is no session to act on.
var ErrNotPaired = errors.New("not paired")

// Logout unlinks this device from the account and deletes session.db. The
// archive is kept (its TTLs still apply); delete archive.db to drop it.
func Logout(ctx context.Context, opts LogoutOptions) error {
	lock, err := AcquireLock(opts.Paths)
	if err != nil {
		return err
	}
	defer lock.Release()

	if _, err := os.Stat(opts.Paths.Session); errors.Is(err, os.ErrNotExist) {
		return ErrNotPaired
	}
	if !opts.Local {
		sess, err := OpenSession(ctx, opts.Paths, opts.Config.DeviceName, SlogLogger{L: opts.Log})
		if err != nil {
			return err
		}
		if !sess.Paired() {
			sess.Close()
			return RemoveSession(opts.Paths)
		}
		if err := sess.Client.Connect(); err != nil {
			sess.Close()
			return fmt.Errorf("logout: connect: %w (use --local to only delete the local keys)", err)
		}
		err = sess.Client.Logout(ctx)
		sess.Close()
		if err != nil {
			return fmt.Errorf("logout: %w (use --local to only delete the local keys)", err)
		}
	}
	if err := RemoveSession(opts.Paths); err != nil {
		return err
	}
	_ = os.Remove(opts.Paths.Status)
	opts.Log.Info("device unlinked", slog.String(yardlogs.KeyEvent, yardlogs.EventWALogout))
	return nil
}
