package wa

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// PairOptions configures Pair.
type PairOptions struct {
	Paths  Paths
	Config Config
	// Phone, when set, links with an 8-character code typed on the phone
	// instead of a QR code (handy on a headless server). International
	// format, digits only or with punctuation: "+55 11 99999-9999".
	Phone string
	// Force discards an existing local session first. The old device
	// then has to be removed by hand under "Linked devices" on the phone.
	Force bool
	Out   io.Writer
	Log   *slog.Logger
	// CatalogWait bounds how long Pair stays connected after linking to
	// collect chat names from the initial sync. Zero means 45s.
	CatalogWait time.Duration
}

// ErrAlreadyPaired is returned when a session exists and Force is unset.
var ErrAlreadyPaired = errors.New("already paired")

// pairDisplayName is what the phone shows while linking by code. WhatsApp
// validates it against a list of browser/OS pairs; this one is accepted.
const pairDisplayName = "Chrome (Linux)"

// Pair links this machine as a companion device of the user's WhatsApp.
func Pair(ctx context.Context, opts PairOptions) error {
	lock, err := AcquireLock(opts.Paths)
	if err != nil {
		return err
	}
	defer lock.Release()

	if opts.Force {
		if err := RemoveSession(opts.Paths); err != nil {
			return err
		}
	}
	sess, err := OpenSession(ctx, opts.Paths, opts.Config.DeviceName, SlogLogger{L: opts.Log})
	if err != nil {
		return err
	}
	defer sess.Close()
	if sess.Paired() {
		return fmt.Errorf("%w as +%s: run 'logout' first, or 'pair --force' to discard the local session", ErrAlreadyPaired, sess.Client.Store.ID.User)
	}

	qrCh, err := sess.Client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("pair: %w", err)
	}
	if err := sess.Client.Connect(); err != nil {
		return fmt.Errorf("pair: connect: %w", err)
	}

	out := opts.Out
	codeRequested := false
	for item := range qrCh {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			if opts.Phone == "" {
				fmt.Fprintln(out, "\nOn your phone: WhatsApp → Settings → Linked devices → Link a device, then scan:")
				qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, out)
				fmt.Fprintf(out, "(a new code appears every %s)\n", item.Timeout.Round(time.Second))
				continue
			}
			if codeRequested {
				continue
			}
			codeRequested = true
			code, err := sess.Client.PairPhone(ctx, opts.Phone, true, whatsmeow.PairClientChrome, pairDisplayName)
			if err != nil {
				return fmt.Errorf("pair: request code for %s: %w", opts.Phone, err)
			}
			fmt.Fprintf(out, "\nOn your phone: WhatsApp → Settings → Linked devices → Link a device →\n"+
				"\"Link with phone number instead\", then type:\n\n    %s\n\n", formatPairCode(code))
		case whatsmeow.QRChannelEventError:
			return fmt.Errorf("pair: %w", item.Error)
		case whatsmeow.QRChannelEventPasskeyRequest, whatsmeow.QRChannelEventPasskeyResponse:
			return errors.New("pair: this account asked for passkey verification, which shipyard cannot answer; try pairing by QR code")
		case whatsmeow.QRChannelSuccess.Event:
			pn := sess.Client.Store.ID.User
			fmt.Fprintf(out, "Linked as +%s.\n", pn)
			opts.Log.Info(yardlogs.EventWAPaired, slog.String(yardlogs.KeyMessage, "device paired"))
			return collectCatalog(ctx, opts, sess)
		case whatsmeow.QRChannelTimeout.Event:
			return errors.New("pair: timed out waiting for the phone; run pair again")
		case whatsmeow.QRChannelClientOutdated.Event:
			return errors.New("pair: WhatsApp rejected this client version; upgrade shipyard-comms-whatsapp")
		case whatsmeow.QRChannelScannedWithoutMultidevice.Event:
			fmt.Fprintln(out, "The phone does not have multi-device enabled; enable it and scan again.")
		default:
			return fmt.Errorf("pair: unexpected state %q", item.Event)
		}
	}
	return errors.New("pair: connection closed before linking finished")
}

// collectCatalog stays connected briefly after linking so the chat
// catalog (contacts, groups, initial-sync chat names) is ready before the
// user runs `chats` or `watch`.
func collectCatalog(ctx context.Context, opts PairOptions, sess *Session) error {
	arch, err := archive.Open(ctx, opts.Paths.Archive)
	if err != nil {
		return err
	}
	defer func() { _ = arch.Close() }()

	wait := opts.CatalogWait
	if wait == 0 {
		wait = 45 * time.Second
	}
	fmt.Fprintf(opts.Out, "Collecting chat names (up to %s)…\n", wait)
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	connected := make(chan struct{}, 1)
	h := &Handler{
		Archive: arch,
		Dir:     sess.Directory(),
		Tracker: NewTracker("", os.Getpid(), nil),
		Log:     opts.Log,
		Async:   func(f func()) { f() },
	}
	sess.Client.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Connected); ok {
			select {
			case connected <- struct{}{}:
			default:
			}
			return // RefreshCatalog runs below, once the sync had time to land
		}
		h.Handle(ctx, evt)
	})
	select {
	case <-connected:
	case <-ctx.Done():
	}
	<-ctx.Done() // let app-state (contact names) and history sync arrive
	h.RefreshCatalog(context.WithoutCancel(ctx))

	counts, err := arch.Counts(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.Out, "%d chats catalogued. Next:\n"+
		"  shipyard comms whatsapp chats --search <name>\n"+
		"  shipyard comms whatsapp watch <chat> --ttl 7d\n"+
		"  shipyard comms whatsapp serve   (or run it as a service)\n", counts.Chats)
	return nil
}

// formatPairCode renders "ABCDEFGH" as "ABCD-EFGH", as the phone shows it.
func formatPairCode(code string) string {
	if len(code) == 8 {
		return code[:4] + "-" + code[4:]
	}
	return code
}

// RemoveSession deletes session.db and its WAL/SHM companions.
func RemoveSession(p Paths) error {
	for _, f := range []string{p.Session, p.Session + "-wal", p.Session + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("whatsapp: remove %s: %w", f, err)
		}
	}
	return nil
}
