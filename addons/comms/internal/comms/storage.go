package comms

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CommsHome resolves the comms-specific state directory. Honors
// SHIPYARD_HOME if set, otherwise falls back to $HOME/.shipyard.
// In both cases the returned path is the "comms" subdirectory of the
// shipyard home — never the root.
//
// Mirrors crewctl.ShipyardHome() in behavior. Duplicated here because
// addon packages must not import the core controllers (see CLAUDE.md
// architecture rules).
func CommsHome() (string, error) {
	if h := os.Getenv("SHIPYARD_HOME"); h != "" {
		return filepath.Join(h, "comms"), nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("comms: resolve home dir: %w", err)
	}
	return filepath.Join(h, ".shipyard", "comms"), nil
}

// ChannelsConfigPath returns the absolute path to channels.json under
// the given comms home (typically the result of CommsHome()).
func ChannelsConfigPath(home string) string {
	return filepath.Join(home, "channels.json")
}

// SecretsDir returns the directory holding per-channel credential
// files under the given comms home.
func SecretsDir(home string) string {
	return filepath.Join(home, "secrets")
}

// SecretPath returns the absolute path to the credentials file for a
// given channel ID. The caller is responsible for validating the ID
// (use channelIDPattern via Channel.Validate or SecretStore.Write,
// which enforces it internally).
func SecretPath(home, channelID string) string {
	return filepath.Join(SecretsDir(home), channelID+".json")
}

// channelsFile is the on-disk JSON envelope around the channels slice.
// Wrapping in an object (rather than a top-level array) leaves room to
// add schema_version, metadata or other top-level fields later without
// a disruptive migration. Same choice fairway made for routes.json.
type channelsFile struct {
	Channels []Channel `json:"channels"`
}

// ChannelStore reads and writes channels.json under a given comms home.
// Operations are atomic at the file level: Save writes to a sibling
// .tmp and renames into place; concurrent readers always see either
// the old or the new file in full, never a torn write.
//
// ChannelStore is NOT goroutine-safe — wrap externally if multiple
// goroutines mutate. (In v1 the addon is single-shot CLI, so
// concurrent access does not arise.)
type ChannelStore struct {
	path string
}

// NewChannelStore returns a store rooted at the given comms home. The
// directory is created on demand (perm 0700) when Save is called;
// Load against a non-existent file returns an empty slice and nil error.
func NewChannelStore(home string) *ChannelStore {
	return &ChannelStore{path: ChannelsConfigPath(home)}
}

// Path returns the absolute path to the channels.json file this store
// reads/writes. Useful for callers wanting to display where they're
// reading from, e.g. in CLI output.
func (s *ChannelStore) Path() string { return s.path }

// Load reads and parses channels.json. Missing file returns
// (empty slice, nil error). Malformed JSON returns an error wrapping
// the parse failure. Each loaded channel is run through
// Channel.Validate; the first invalid one aborts the load with an
// error naming the channel ID.
func (s *ChannelStore) Load() ([]Channel, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("comms: read %s: %w", s.path, err)
	}
	var f channelsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("comms: parse %s: %w", s.path, err)
	}
	for i, c := range f.Channels {
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("comms: %s: channel[%d] invalid: %w", s.path, i, err)
		}
	}
	return f.Channels, nil
}

// Save writes the given channels atomically. Validation runs first;
// the disk is untouched if any channel fails Validate or if any two
// channels share a Name (case-insensitive). On success the file ends
// up with perm 0644 (config, not secret) and the parent directory is
// created with perm 0700 if it does not exist.
//
// channels == nil is treated as an empty slice (writes the envelope
// with an empty list, not a missing file).
func (s *ChannelStore) Save(channels []Channel) error {
	for i, c := range channels {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("comms: channels[%d]: %w", i, err)
		}
	}
	if first, second := FindDuplicateName(channels); first != nil {
		return fmt.Errorf("comms: duplicate channel name %q used by %s and %s",
			first.Name, first.ID, second.ID)
	}

	if channels == nil {
		channels = []Channel{}
	}
	payload, err := json.MarshalIndent(channelsFile{Channels: channels}, "", "  ")
	if err != nil {
		return fmt.Errorf("comms: marshal channels: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("comms: create %s: %w", dir, err)
	}

	return writeAtomic(s.path, payload, 0o644)
}

// writeAtomic writes data to path via a sibling .tmp file plus rename.
// The temp file is created in the destination directory so the rename
// is atomic on the same filesystem. perm is applied with Chmod before
// rename (CreateTemp ignores the supplied mode on most platforms).
//
// On any failure path the temp file is removed via deferred cleanup.
// Mirrors the pattern in addons/fairway/internal/fairway/config.go.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	tmp, err := os.CreateTemp(dir, base+".*.tmp")
	if err != nil {
		return fmt.Errorf("comms: create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	var committed bool
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("comms: write temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("comms: sync temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("comms: close temp file %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("comms: chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("comms: rename %s → %s: %w", tmpPath, path, err)
	}
	committed = true
	return nil
}
