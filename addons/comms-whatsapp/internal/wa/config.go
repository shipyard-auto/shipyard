package wa

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ConfigVersion is the on-disk schema of config.json.
const ConfigVersion = 1

// Config is ~/.shipyard/comms/whatsapp/config.json. Per-chat settings
// (watched, ttl) live in archive.db instead, because the CLI flips them
// while the daemon is running and the daemon reads them on every event.
type Config struct {
	Version int `json:"version"`
	// DeviceName is how the linked device shows up under "Linked devices"
	// on the phone. Only read at pairing time.
	DeviceName string `json:"device_name"`
	// DefaultTTL applies to watched chats without their own ttl.
	DefaultTTL string `json:"default_ttl"`

	// Send policy, enforced by the daemon on every send. Fields absent
	// from an older config.json keep their defaults.
	Limits SendLimits `json:"limits"`
	// MaxChars caps one message's length.
	MaxChars int `json:"max_chars"`
	// Humanize shows "typing…" for a length-proportional delay before
	// each send, like a person would.
	Humanize bool `json:"humanize"`
	// Paused blocks every send (archiving continues). Read on each send,
	// so `pause` takes effect without restarting the daemon.
	Paused bool `json:"paused"`
	// AllowGroups permits sending to groups that are also allowlisted.
	// Off by default: posting into groups is the riskiest pattern, so it
	// must be an explicit choice (e.g. publishing to your own groups).
	AllowGroups bool `json:"allow_groups"`

	// Inbound delivery: received messages of routed chats are POSTed to
	// fairway as comms Message envelopes, so agents can react to them.
	Routes Routes `json:"routes"`
	// PublishEvents also publishes every new message (any chat) to fairway's
	// events API as "whatsapp.message", scoped "whatsapp.read:<chat>", for
	// signed webhook subscribers. Off by default.
	PublishEvents bool `json:"publish_events"`
	// FairwayAddr overrides where events go ("host:port"). Empty means
	// "read bind/port from fairway's routes.json", defaulting to
	// 127.0.0.1:9876.
	FairwayAddr string `json:"fairway_addr,omitempty"`

	// APIAddr is where the daemon serves the query API ("127.0.0.1:9877"),
	// reached by external systems through a signed fairway route. Empty =
	// off. Loopback only. Read at daemon start.
	APIAddr string `json:"api_addr,omitempty"`
}

// DefaultAPIAddr is what 'api enable' uses without --addr.
const DefaultAPIAddr = "127.0.0.1:9877"

// Routes maps chats to fairway paths.
type Routes struct {
	// ByChat maps a chat JID to a fairway path ("/wa/mae").
	ByChat map[string]string `json:"by_chat,omitempty"`
	// Default receives every other chat's messages; empty = none.
	Default string `json:"default,omitempty"`
	// IncludeOwn also delivers messages the account itself sent (from the
	// phone). Off by default: agents usually react to others.
	IncludeOwn bool `json:"include_own,omitempty"`
}

// PathFor returns the fairway path for chatJID, or "" when unrouted.
func (r Routes) PathFor(chatJID string) string {
	if p, ok := r.ByChat[chatJID]; ok {
		return p
	}
	return r.Default
}

func validRoutePath(p string) error {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " ?#") || len(p) > 200 {
		return fmt.Errorf("route path %q must start with / and contain no spaces, ? or #", p)
	}
	return nil
}

// SendLimits are rate limits written as "<count>/<window>", e.g. "6/10m".
type SendLimits struct {
	PerChat  string `json:"per_chat"`  // each contact (and the self chat)
	PerGroup string `json:"per_group"` // each group
	Global   string `json:"global"`    // all chats together
}

// Default limits. Conservative for a personal number: a contact gets a
// short conversation's worth, a group a steady posting rhythm.
const (
	DefaultLimitPerChat  = "6/10m"
	DefaultLimitPerGroup = "20/1h"
	DefaultLimitGlobal   = "60/1h"
)

// Limits are the parsed send limits.
type Limits struct {
	PerChat, PerGroup, Global Rate
}

// ForKind returns the per-chat limit that applies to a chat kind.
func (l Limits) ForKind(group bool) Rate {
	if group {
		return l.PerGroup
	}
	return l.PerChat
}

// Rate is a parsed "<count>/<window>" limit.
type Rate struct {
	Count  int
	Window time.Duration
}

func (r Rate) String() string { return fmt.Sprintf("%d/%s", r.Count, FormatDuration(r.Window)) }

// Human renders the rate for people: "20 per hour", "6 per 10 minutes".
func (r Rate) Human() string {
	unit := func(n int64, one, many string) string {
		if n == 1 {
			return one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	var w string
	switch {
	case r.Window%(24*time.Hour) == 0:
		w = unit(int64(r.Window/(24*time.Hour)), "day", "days")
	case r.Window%time.Hour == 0:
		w = unit(int64(r.Window/time.Hour), "hour", "hours")
	default:
		w = unit(int64(r.Window/time.Minute), "minute", "minutes")
	}
	return fmt.Sprintf("%d per %s", r.Count, w)
}

// windowUnits maps the spelled-out units ParseRate accepts to Go's.
var windowUnits = map[string]string{
	"m": "m", "min": "m", "mins": "m", "minute": "m", "minutes": "m",
	"h": "h", "hr": "h", "hour": "h", "hours": "h",
	"d": "d", "day": "d", "days": "d",
}

// ParseRate parses "<count>/<window>": "6/10m", "20/1h", "20/h",
// "60/hour", "100/1d". A bare unit means one of it.
func ParseRate(s string) (Rate, error) {
	n, w, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return Rate{}, fmt.Errorf("invalid rate %q: want <count>/<window>, e.g. 6/10m, 20/h", s)
	}
	w = strings.ToLower(strings.TrimSpace(w))
	if i := strings.IndexFunc(w, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		num, u := w[:i], strings.TrimSpace(w[i:])
		if std, ok := windowUnits[u]; ok {
			if num == "" {
				num = "1"
			}
			w = num + std
		}
	}
	count, err := strconv.Atoi(strings.TrimSpace(n))
	if err != nil || count < 1 {
		return Rate{}, fmt.Errorf("invalid rate %q: count must be a positive integer", s)
	}
	window, err := ParseDuration(w)
	if err != nil || window < time.Minute || window > 24*time.Hour {
		return Rate{}, fmt.Errorf("invalid rate %q: window must be between 1m and 1d", s)
	}
	return Rate{Count: count, Window: window}, nil
}

// MaxMessageChars is the hard ceiling for MaxChars (WhatsApp's own limit
// is larger; a bot has no business sending walls of text).
const MaxMessageChars = 4096

// DefaultConfig returns the configuration used when config.json is absent.
func DefaultConfig() Config {
	return Config{
		Version:    ConfigVersion,
		DeviceName: "Shipyard",
		DefaultTTL: FormatDuration(DefaultTTL),
		Limits:     SendLimits{PerChat: DefaultLimitPerChat, PerGroup: DefaultLimitPerGroup, Global: DefaultLimitGlobal},
		MaxChars:   MaxMessageChars,
		Humanize:   true,
	}
}

// Validate checks every field.
func (c Config) Validate() error {
	if c.Version != ConfigVersion {
		return fmt.Errorf("config: unsupported version %d (expected %d)", c.Version, ConfigVersion)
	}
	if strings.TrimSpace(c.DeviceName) == "" || len(c.DeviceName) > 40 {
		return errors.New("config: device_name must be 1-40 characters")
	}
	if _, err := ParseTTL(c.DefaultTTL); err != nil {
		return fmt.Errorf("config: default_ttl: %w", err)
	}
	if _, err := ParseRate(c.Limits.PerChat); err != nil {
		return fmt.Errorf("config: limits.per_chat: %w", err)
	}
	if _, err := ParseRate(c.Limits.PerGroup); err != nil {
		return fmt.Errorf("config: limits.per_group: %w", err)
	}
	if _, err := ParseRate(c.Limits.Global); err != nil {
		return fmt.Errorf("config: limits.global: %w", err)
	}
	if c.MaxChars < 1 || c.MaxChars > MaxMessageChars {
		return fmt.Errorf("config: max_chars must be between 1 and %d", MaxMessageChars)
	}
	for jid, p := range c.Routes.ByChat {
		if err := validRoutePath(p); err != nil {
			return fmt.Errorf("config: routes.by_chat[%s]: %w", jid, err)
		}
	}
	if c.Routes.Default != "" {
		if err := validRoutePath(c.Routes.Default); err != nil {
			return fmt.Errorf("config: routes.default: %w", err)
		}
	}
	if c.FairwayAddr != "" {
		if _, _, err := net.SplitHostPort(c.FairwayAddr); err != nil {
			return fmt.Errorf("config: fairway_addr %q: want host:port", c.FairwayAddr)
		}
	}
	if c.APIAddr != "" {
		if err := ValidateAPIAddr(c.APIAddr); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	return nil
}

// ParsedLimits returns the parsed limits of a validated Config.
func (c Config) ParsedLimits() Limits {
	var l Limits
	l.PerChat, _ = ParseRate(c.Limits.PerChat)
	l.PerGroup, _ = ParseRate(c.Limits.PerGroup)
	l.Global, _ = ParseRate(c.Limits.Global)
	return l
}

// DefaultTTLDuration returns DefaultTTL parsed. Callers hold a validated
// Config, so a parse failure falls back to the package default.
func (c Config) DefaultTTLDuration() time.Duration {
	d, err := ParseTTL(c.DefaultTTL)
	if err != nil {
		return DefaultTTL
	}
	return d
}

// Set updates one field by its CLI key.
func (c *Config) Set(key, value string) error {
	switch key {
	case "device-name":
		c.DeviceName = value
	case "default-ttl":
		c.DefaultTTL = value
	case "limit-per-chat":
		c.Limits.PerChat = value
	case "limit-per-group":
		c.Limits.PerGroup = value
	case "limit-global":
		c.Limits.Global = value
	case "allow-groups":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("config: allow-groups: %q is not true/false", value)
		}
		c.AllowGroups = b
	case "max-chars":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("config: max-chars: %q is not a number", value)
		}
		c.MaxChars = n
	case "fairway-addr":
		c.FairwayAddr = value
	case "api-addr":
		if value == "off" {
			value = ""
		}
		c.APIAddr = value
	case "publish-events":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("config: publish-events: %q is not true/false", value)
		}
		c.PublishEvents = b
	case "include-own":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("config: include-own: %q is not true/false", value)
		}
		c.Routes.IncludeOwn = b
	case "humanize":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("config: humanize: %q is not true/false", value)
		}
		c.Humanize = b
	default:
		return fmt.Errorf("config: unknown key %q (known: %s)", key, strings.Join(ConfigKeys, ", "))
	}
	return c.Validate()
}

// ConfigKeys are the keys accepted by Set. `paused` is deliberately not
// one of them: pause/resume have their own commands.
var ConfigKeys = []string{"device-name", "default-ttl", "limit-per-chat", "limit-per-group", "limit-global",
	"allow-groups", "max-chars", "humanize", "fairway-addr", "include-own", "publish-events", "api-addr"}

// LoadConfig reads path, returning DefaultConfig when it does not exist.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	// Decode over the defaults so fields added after a file was written
	// keep sensible values.
	c := DefaultConfig()
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// SaveConfig validates c and writes it atomically with mode 0600.
func SaveConfig(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	return writeFileAtomic(path, append(raw, '\n'))
}

// writeFileAtomic writes data to a temp file in path's directory and
// renames it over path, so readers never observe a partial file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ConfigCache serves config.json, re-reading it only when the file's
// modification time changes. The daemon consults it per message and per
// send; this keeps CLI edits (route, pause, limits) live without a restart
// and without a disk read for every event.
type ConfigCache struct {
	path  string
	mu    sync.Mutex
	mtime time.Time
	size  int64
	cfg   Config
	err   error
	ok    bool
}

// NewConfigCache returns a cache over path.
func NewConfigCache(path string) *ConfigCache { return &ConfigCache{path: path} }

// Get returns the current config. A missing file yields DefaultConfig. A
// file that fails to parse keeps serving the last good config (and the
// error), so a half-written edit never stops the daemon.
func (c *ConfigCache) Get() (Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var mtime time.Time
	var size int64
	if info, err := os.Stat(c.path); err == nil {
		mtime, size = info.ModTime(), info.Size()
	}
	if c.ok && mtime.Equal(c.mtime) && size == c.size {
		return c.cfg, c.err
	}
	cfg, err := LoadConfig(c.path)
	c.mtime, c.size = mtime, size
	if err != nil {
		if !c.ok {
			return DefaultConfig(), err
		}
		c.err = err
		return c.cfg, err
	}
	c.cfg, c.err, c.ok = cfg, nil, true
	return cfg, nil
}
