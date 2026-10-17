package wa

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

// DefaultConfig returns the configuration used when config.json is absent.
func DefaultConfig() Config {
	return Config{
		Version:    ConfigVersion,
		DeviceName: "Shipyard",
		DefaultTTL: FormatDuration(DefaultTTL),
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
	return nil
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
	default:
		return fmt.Errorf("config: unknown key %q (known: device-name, default-ttl)", key)
	}
	return c.Validate()
}

// LoadConfig reads path, returning DefaultConfig when it does not exist.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	var c Config
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
