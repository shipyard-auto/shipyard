package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/app"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

func loadConfig(e env) (wa.Config, error) {
	return wa.LoadConfig(e.paths.Config)
}

// logger writes to ~/.shipyard/logs/comms/. withStderr also mirrors
// warnings to stderr, where the service manager's journal picks them up.
func logger(withStderr bool) (*slog.Logger, func()) {
	store, err := yardlogs.DefaultStore()
	if err != nil || store == nil {
		return slog.New(yardlogs.NopHandler()), func() {}
	}
	opts := yardlogs.Options{Store: store, Version: app.Version}
	if withStderr {
		opts.Extra = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})
	}
	return yardlogs.New(yardlogs.SourceComms, opts), func() { _ = store.Close() }
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ── pair ─────────────────────────────────────────────────────────────────

func runPair(ctx context.Context, e env, args []string) error {
	fs := newFlags("pair", e)
	phone := fs.String("phone", "", "link with a code typed on the phone instead of a QR (international format, e.g. +5511999999999)")
	force := fs.Bool("force", false, "discard an existing local session first")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	log, done := logger(false)
	defer done()
	err = wa.Pair(ctx, wa.PairOptions{
		Paths: e.paths, Config: cfg, Phone: *phone, Force: *force, Out: e.stdout, Log: log,
	})
	if *force && err == nil {
		fmt.Fprintln(e.stdout, "If an older Shipyard device is still listed under Linked devices on the phone, remove it there.")
	}
	return err
}

// ── serve ────────────────────────────────────────────────────────────────

func runServe(ctx context.Context, e env, args []string) error {
	fs := newFlags("serve", e)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return &wa.ExitError{Code: wa.ExitRuntime, Err: err}
	}
	log, done := logger(true)
	defer done()
	return wa.Serve(ctx, wa.ServeOptions{Paths: e.paths, Config: cfg, Log: log})
}

// ── status ───────────────────────────────────────────────────────────────

func runStatus(ctx context.Context, e env, args []string) error {
	fs := newFlags("status", e)
	asJSON := fs.Bool("json", false, "emit JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	now := time.Now()
	r, err := wa.BuildReport(ctx, e.paths, cfg, now)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(e.stdout, r)
	}
	printReport(e.stdout, r, now)
	return nil
}

func printReport(w io.Writer, r wa.Report, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer func() { _ = tw.Flush() }()

	paired := "no — run 'shipyard-comms-whatsapp pair'"
	if r.Paired {
		paired = "yes, +" + r.Account
		if r.PushName != "" {
			paired += " (" + r.PushName + ")"
		}
	}
	fmt.Fprintf(tw, "Paired:\t%s\n", paired)

	switch {
	case r.Daemon == nil:
		fmt.Fprintf(tw, "Daemon:\tnever started\n")
	case r.Running:
		d := r.Daemon
		fmt.Fprintf(tw, "Daemon:\trunning (pid %d), %s for %s\n", d.PID, d.State, now.Sub(d.Since).Round(time.Second))
		if d.Detail != "" {
			fmt.Fprintf(tw, "\t%s\n", d.Detail)
		}
		if !d.LastMessageAt.IsZero() {
			fmt.Fprintf(tw, "Last message:\t%s ago\n", now.Sub(d.LastMessageAt).Round(time.Second))
		}
	default:
		d := r.Daemon
		fmt.Fprintf(tw, "Daemon:\tnot running (last state %s at %s)\n", d.State, d.HeartbeatAt.Local().Format("2006-01-02 15:04"))
		if d.Detail != "" {
			fmt.Fprintf(tw, "\t%s\n", d.Detail)
		}
	}
	fmt.Fprintf(tw, "Archive:\t%d watched of %d chats, %d messages\n", r.Archive.Watched, r.Archive.Chats, r.Archive.Messages)
	fmt.Fprintf(tw, "Default TTL:\t%s\n", r.Config.DefaultTTL)
	l := r.Config.ParsedLimits()
	sending := fmt.Sprintf("%d allowed chats, %d sent in the last hour; limits: %s per contact, %s per group, %s overall (see 'limits')",
		r.Archive.SendAllowed, r.Archive.SentLastHour, l.PerChat.Human(), l.PerGroup.Human(), l.Global.Human())
	if r.Config.Paused {
		sending = "PAUSED — " + sending
	}
	fmt.Fprintf(tw, "Sending:\t%s\n", sending)
	routes := len(r.Config.Routes.ByChat)
	if r.Config.Routes.Default != "" {
		routes++
	}
	receiving := fmt.Sprintf("%d routes to fairway", routes)
	if r.Daemon != nil && (r.Daemon.Delivered > 0 || r.Daemon.DeliveryFailed > 0) {
		receiving += fmt.Sprintf(", %d delivered, %d failed since daemon start", r.Daemon.Delivered, r.Daemon.DeliveryFailed)
	}
	fmt.Fprintf(tw, "Receiving:\t%s\n", receiving)
}

// ── chats ────────────────────────────────────────────────────────────────

func runChats(ctx context.Context, e env, args []string) error {
	fs := newFlags("chats", e)
	search := fs.String("search", "", "filter by name or JID (ignores case and accents)")
	watched := fs.Bool("watched", false, "only watched chats")
	asJSON := fs.Bool("json", false, "emit JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	arch, err := openArchive(ctx, e)
	if err != nil {
		return err
	}
	defer func() { _ = arch.Close() }()
	chats, err := arch.ListChats(ctx, archive.ListFilter{Search: *search, WatchedOnly: *watched})
	if err != nil {
		return err
	}
	if *asJSON {
		type row struct {
			archive.Chat
			TTL string `json:"ttl,omitempty"`
		}
		out := make([]row, 0, len(chats))
		for _, c := range chats {
			out = append(out, row{Chat: c, TTL: chatTTL(c, cfg)})
		}
		return writeJSON(e.stdout, out)
	}
	if len(chats) == 0 {
		fmt.Fprintln(e.stdout, "No chats. Pair first, or widen --search.")
		return nil
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tWATCHED\tTTL\tMESSAGES\tSEND\tJID")
	for _, c := range chats {
		w, s := "", ""
		if c.Watched {
			w = "yes"
		}
		if c.SendAllowed {
			s = "allowed"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", c.Name, c.Kind, w, chatTTL(c, cfg), c.Messages, s, c.JID)
	}
	return tw.Flush()
}

func chatTTL(c archive.Chat, cfg wa.Config) string {
	switch {
	case !c.Watched:
		return ""
	case c.TTL > 0:
		return wa.FormatDuration(c.TTL)
	default:
		return cfg.DefaultTTL + " (default)"
	}
}

func openArchive(ctx context.Context, e env) (*archive.Store, error) {
	if err := e.paths.Ensure(); err != nil {
		return nil, err
	}
	return archive.Open(ctx, e.paths.Archive)
}

// ── watch / unwatch ──────────────────────────────────────────────────────

func runWatch(ctx context.Context, e env, args []string) error {
	fs := newFlags("watch", e)
	ttlFlag := fs.String("ttl", "", "how long to keep messages (e.g. 7d, 36h); default from config")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: watch <chat> [--ttl 7d]")
	}
	var ttl time.Duration
	if *ttlFlag != "" {
		if ttl, err = wa.ParseTTL(*ttlFlag); err != nil {
			return err
		}
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	arch, err := openArchive(ctx, e)
	if err != nil {
		return err
	}
	defer func() { _ = arch.Close() }()

	chat, err := resolveChat(ctx, arch, pos[0])
	if errors.Is(err, archive.ErrNotFound) {
		jid, ok := phoneJID(pos[0])
		if !ok {
			return err
		}
		if err := arch.UpsertChat(ctx, jid, "", archive.KindDM); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "%s is not in the catalog yet; added by number.\n", jid)
		chat, err = arch.GetChat(ctx, jid)
	}
	if err != nil {
		return err
	}
	if err := arch.Watch(ctx, chat.JID, ttl); err != nil {
		return err
	}
	shown := "the default " + cfg.DefaultTTL
	if ttl > 0 {
		shown = wa.FormatDuration(ttl)
	}
	fmt.Fprintf(e.stdout, "Watching %s (%s); messages kept for %s.\n", chat.Name, chat.JID, shown)
	fmt.Fprintln(e.stdout, "Only messages received from now on are archived.")
	return nil
}

func runUnwatch(ctx context.Context, e env, args []string) error {
	fs := newFlags("unwatch", e)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: unwatch <chat>")
	}
	arch, err := openArchive(ctx, e)
	if err != nil {
		return err
	}
	defer func() { _ = arch.Close() }()
	chat, err := resolveChat(ctx, arch, pos[0])
	if err != nil {
		return err
	}
	n, err := arch.Unwatch(ctx, chat.JID)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Stopped watching %s; deleted %d archived messages.\n", chat.Name, n)
	return nil
}

// resolveChat accepts a JID, a chat name or a phone number.
func resolveChat(ctx context.Context, arch *archive.Store, query string) (archive.Chat, error) {
	return wa.ResolveChat(ctx, arch, query)
}

// phoneJID turns a phone number into a JID.
func phoneJID(s string) (string, bool) { return wa.PhoneJID(s) }

// ── history ──────────────────────────────────────────────────────────────

func runHistory(ctx context.Context, e env, args []string) error {
	fs := newFlags("history", e)
	sinceFlag := fs.String("since", "24h", "how far back to read (e.g. 2h, 3d)")
	limit := fs.Int("limit", 200, "maximum number of messages (newest kept); 0 = no limit")
	asJSON := fs.Bool("json", false, "emit JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: history <chat> [--since 24h] [--limit 200] [--json]")
	}
	since, err := wa.ParseDuration(*sinceFlag)
	if err != nil {
		return err
	}
	arch, err := openArchive(ctx, e)
	if err != nil {
		return err
	}
	defer func() { _ = arch.Close() }()
	chat, err := resolveChat(ctx, arch, pos[0])
	if err != nil {
		return err
	}
	msgs, err := arch.History(ctx, chat.JID, time.Now().Add(-since), *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(e.stdout, struct {
			Chat     archive.Chat      `json:"chat"`
			Messages []archive.Message `json:"messages"`
		}{chat, nonNil(msgs)})
	}
	if !chat.Watched {
		fmt.Fprintf(e.stderr, "%s is not watched; run 'watch %q' to start archiving it.\n", chat.Name, chat.Name)
	}
	for _, m := range msgs {
		fmt.Fprintln(e.stdout, formatMessage(m))
	}
	return nil
}

func nonNil(m []archive.Message) []archive.Message {
	if m == nil {
		return []archive.Message{}
	}
	return m
}

func formatMessage(m archive.Message) string {
	who := m.SenderName
	switch {
	case m.FromMe:
		who = "me"
	case who == "":
		who = strings.TrimSuffix(m.SenderJID, "@s.whatsapp.net")
	}
	body := m.Text
	if m.Kind != "text" {
		body = strings.TrimSpace("[" + m.Kind + "] " + m.Text)
	}
	return fmt.Sprintf("%s  %s: %s", m.Timestamp.Local().Format("2006-01-02 15:04"), who, body)
}

// ── logout ───────────────────────────────────────────────────────────────

func runLogout(ctx context.Context, e env, args []string) error {
	fs := newFlags("logout", e)
	local := fs.Bool("local", false, "only delete the local keys (offline); remove the device on the phone yourself")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	log, done := logger(false)
	defer done()
	if err := wa.Logout(ctx, wa.LogoutOptions{Paths: e.paths, Config: cfg, Log: log, Local: *local}); err != nil {
		return err
	}
	fmt.Fprintln(e.stdout, "Unlinked. The archive was kept and still expires by TTL; delete archive.db to drop it now.")
	return nil
}

// ── config ───────────────────────────────────────────────────────────────

func runConfig(_ context.Context, e env, args []string) error {
	args, err := parse(newFlags("config", e), args)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "show"):
		return writeJSON(e.stdout, cfg)
	case len(args) == 3 && args[0] == "set":
		if err := cfg.Set(args[1], args[2]); err != nil {
			return err
		}
		if err := e.paths.Ensure(); err != nil {
			return err
		}
		if err := wa.SaveConfig(e.paths.Config, cfg); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "%s = %s\n", args[1], args[2])
		if args[1] == "device-name" {
			fmt.Fprintln(e.stdout, "The device name only applies to the next pairing.")
		}
		return nil
	default:
		return fmt.Errorf("usage: config [show] | config set <key> <value>; keys: %s", strings.Join(wa.ConfigKeys, ", "))
	}
}
