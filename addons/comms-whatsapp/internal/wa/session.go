package wa

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// Session is the linked device: its key store (session.db) and the
// whatsmeow client bound to it. Hold the session Lock while a Session is
// open.
type Session struct {
	container *sqlstore.Container
	Client    *whatsmeow.Client
}

// OpenSession opens (creating if needed) session.db and builds a client
// for its first device. deviceName labels the device under "Linked
// devices" on the phone; it only matters for a fresh pairing.
func OpenSession(ctx context.Context, p Paths, deviceName string, log waLog.Logger) (*Session, error) {
	if err := p.Ensure(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p.Session, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: create %s: %w", p.Session, err)
	}
	_ = f.Close()
	if log == nil {
		log = waLog.Noop
	}
	db, err := sql.Open("sqlite", "file:"+p.Session+
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("whatsapp: open session store: %w", err)
	}
	container := sqlstore.NewWithDB(db, "sqlite", log.Sub("store"))
	if err := container.Upgrade(ctx); err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("whatsapp: upgrade session store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("whatsapp: load device: %w", err)
	}

	// Identify as a desktop companion named after the config. Package
	// globals in whatsmeow; set before the first Connect.
	store.SetOSInfo(deviceName, [3]uint32{0, 1, 0})
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
	store.DeviceProps.RequireFullSync = proto.Bool(false)

	// Never call SendPresence(available): a companion marked available
	// makes WhatsApp stop pushing notifications to the phone, and nobody
	// reads messages on this device. whatsmeow sends no presence by itself.
	client := whatsmeow.NewClient(device, log.Sub("client"))
	return &Session{container: container, Client: client}, nil
}

// Paired reports whether the store holds a logged-in device.
func (s *Session) Paired() bool { return s.Client.Store.ID != nil }

// Close disconnects and closes the store.
func (s *Session) Close() {
	s.Client.Disconnect()
	_ = s.container.Close()
}

// Directory returns the store-backed Directory for this session.
func (s *Session) Directory() Directory { return storeDirectory{cli: s.Client} }

type storeDirectory struct{ cli *whatsmeow.Client }

func (d storeDirectory) Own() (types.JID, types.JID) {
	if d.cli.Store.ID == nil {
		return types.JID{}, d.cli.Store.LID
	}
	return d.cli.Store.ID.ToNonAD(), d.cli.Store.LID.ToNonAD()
}

func (d storeDirectory) OwnPushName() string { return d.cli.Store.PushName }

func (d storeDirectory) PNForLID(ctx context.Context, lid types.JID) (types.JID, bool) {
	pn, err := d.cli.Store.LIDs.GetPNForLID(ctx, lid)
	if err != nil || pn.IsEmpty() {
		return types.JID{}, false
	}
	return pn, true
}

func (d storeDirectory) ContactName(ctx context.Context, jid types.JID) string {
	info, err := d.cli.Store.Contacts.GetContact(ctx, jid)
	if err != nil || !info.Found {
		return ""
	}
	return contactName(info)
}

func (d storeDirectory) GroupName(ctx context.Context, jid types.JID) string {
	info, err := d.cli.GetGroupInfo(ctx, jid)
	if err != nil || info == nil {
		return ""
	}
	return info.Name
}

func (d storeDirectory) Contacts(ctx context.Context) (map[types.JID]string, error) {
	all, err := d.cli.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[types.JID]string, len(all))
	for jid, info := range all {
		if name := contactName(info); name != "" {
			out[jid] = name
		}
	}
	return out, nil
}

func (d storeDirectory) Groups(ctx context.Context) (map[types.JID]string, error) {
	groups, err := d.cli.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[types.JID]string, len(groups))
	for _, g := range groups {
		out[g.JID] = g.Name
	}
	return out, nil
}

// contactName prefers the name saved in the phone's address book.
func contactName(info types.ContactInfo) string {
	for _, n := range []string{info.FullName, info.FirstName, info.PushName, info.BusinessName} {
		if n = strings.TrimSpace(n); n != "" {
			return n
		}
	}
	return ""
}

// SlogLogger adapts whatsmeow's logger to slog, keeping warnings and
// errors only: whatsmeow's info/debug output is protocol chatter.
type SlogLogger struct {
	L      *slog.Logger
	Module string
}

var _ waLog.Logger = SlogLogger{}

func (s SlogLogger) Warnf(msg string, args ...any)  { s.log(slog.LevelWarn, msg, args) }
func (s SlogLogger) Errorf(msg string, args ...any) { s.log(slog.LevelError, msg, args) }
func (SlogLogger) Infof(string, ...any)             {}
func (SlogLogger) Debugf(string, ...any)            {}
func (s SlogLogger) Sub(module string) waLog.Logger {
	if s.Module != "" {
		module = s.Module + "/" + module
	}
	return SlogLogger{L: s.L, Module: module}
}

func (s SlogLogger) log(level slog.Level, msg string, args []any) {
	s.L.Log(context.Background(), level, yardlogs.EventWAClientLog,
		slog.String(yardlogs.KeyMessage, fmt.Sprintf(msg, args...)),
		slog.String("module", s.Module))
}
