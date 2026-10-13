package wa

import (
	"context"
	"errors"
	"os"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

// Report is what `status` prints.
type Report struct {
	Paired   bool           `json:"paired"`
	Account  string         `json:"account,omitempty"`
	PushName string         `json:"push_name,omitempty"`
	Running  bool           `json:"running"`
	Daemon   *Status        `json:"daemon,omitempty"` // last status.json, even if stale
	Archive  archive.Counts `json:"archive"`
	Config   Config         `json:"config"`
}

// BuildReport gathers status without touching the daemon: status.json for
// the daemon, session.db for the pairing, archive.db for the counts. It
// never creates files that do not exist yet.
func BuildReport(ctx context.Context, p Paths, cfg Config, now time.Time) (Report, error) {
	r := Report{Config: cfg}

	st, ok, err := ReadStatus(p.Status)
	if err != nil {
		return r, err
	}
	if ok {
		r.Daemon = &st
		r.Running = st.Running(now)
		r.Account, r.PushName = st.Account, st.PushName
	}

	if _, err := os.Stat(p.Session); err == nil {
		sess, err := OpenSession(ctx, p, cfg.DeviceName, waLog.Noop)
		if err != nil {
			return r, err
		}
		r.Paired = sess.Paired()
		if r.Paired {
			r.Account = sess.Client.Store.ID.User
			if r.PushName == "" {
				r.PushName = sess.Client.Store.PushName
			}
		}
		_ = sess.container.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return r, err
	}

	if _, err := os.Stat(p.Archive); err == nil {
		arch, err := archive.Open(ctx, p.Archive)
		if err != nil {
			return r, err
		}
		r.Archive, err = arch.Counts(ctx)
		_ = arch.Close()
		if err != nil {
			return r, err
		}
	}
	return r, nil
}
