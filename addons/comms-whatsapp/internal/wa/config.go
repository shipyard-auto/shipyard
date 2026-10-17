package wa

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
}

// SendLimits are rate limits written as "<count>/<window>", e.g. "6/10m".
type SendLimits struct {
	PerChat string `json:"per_chat"`
	Global  string `json:"global"`
}

// Rate is a parsed "<count>/<window>" limit.
type Rate struct {
	Count  int
	Window time.Duration
}

func (r Rate) String() string { return fmt.Sprintf("%d/%s", r.Count, FormatDuration(r.Window)) }

// ParseRate parses "6/10m" into Rate{6, 10m}.
func ParseRate(s string) (Rate, error) {
	n, w, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return Rate{}, fmt.Errorf("invalid rate %q: want <count>/<window>, e.g. 6/10m", s)
	}
	count, err := strconv.Atoi(n)
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
		Limits:     SendLimits{PerChat: "6/10m", Global: "30/1h"},
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
	if _, err := ParseRate(c.Limits.Global); err != nil {
		return fmt.Errorf("config: limits.global: %w", err)
	}
	if c.MaxChars < 1 || c.MaxChars > MaxMessageChars {
		return fmt.Errorf("config: max_chars must be between 1 and %d", MaxMessageChars)
	}
	return nil
}

// Rates returns the parsed limits of a validated Config.
func (c Config) Rates() (perChat, global Rate) {
	perChat, _ = ParseRate(c.Limits.PerChat)
	global, _ = ParseRate(c.Limits.Global)
	return perChat, global
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
	case "limit-global":
		c.Limits.Global = value
	case "max-chars":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("config: max-chars: %q is not a number", value)
		}
		c.MaxChars = n
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
var ConfigKeys = []string{"device-name", "default-ttl", "limit-per-chat", "limit-global", "max-chars", "humanize"}

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
