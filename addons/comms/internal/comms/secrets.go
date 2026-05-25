package comms

import (
	"fmt"
	"os"
	"path/filepath"
)

// SecretStore persists per-channel credentials as flat JSON files under
// SecretsDir(home). In v1 files are stored in plaintext with perm 0600.
// Keyring / encryption integration is anchored debt in
// docs/comms/plano.md.
type SecretStore struct {
	home string
}

// NewSecretStore returns a store rooted at the given comms home.
func NewSecretStore(home string) *SecretStore {
	return &SecretStore{home: home}
}

// Path returns the absolute path of the credential file for a channel
// ID under this store's home. Useful for diagnostics; not validated.
func (s *SecretStore) Path(channelID string) string {
	return SecretPath(s.home, channelID)
}

// Read returns the raw credential JSON for the given channel ID, or
// (nil, os.ErrNotExist) wrapped if no credential file exists. The
// caller interprets the JSON shape — providers define their own
// credential schema (e.g. {"token": "..."} for Telegram).
//
// The channel ID is validated against channelIDPattern; an invalid ID
// returns an error without touching disk.
func (s *SecretStore) Read(channelID string) ([]byte, error) {
	if !channelIDPattern.MatchString(channelID) {
		return nil, fmt.Errorf("comms: secret: invalid channel id %q", channelID)
	}
	path := SecretPath(s.home, channelID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("comms: read secret %s: %w", path, err)
	}
	return data, nil
}

// Write persists the given raw credential JSON atomically, with perm
// 0600. Creates SecretsDir with perm 0700 if needed. The channelID is
// validated; invalid IDs return an error without touching disk.
//
// Caller is responsible for ensuring data is valid JSON for the target
// provider — this layer is opaque to the credential schema.
func (s *SecretStore) Write(channelID string, data []byte) error {
	if !channelIDPattern.MatchString(channelID) {
		return fmt.Errorf("comms: secret: invalid channel id %q", channelID)
	}
	dir := SecretsDir(s.home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("comms: create %s: %w", dir, err)
	}
	return writeAtomic(filepath.Join(dir, channelID+".json"), data, 0o600)
}

// Delete removes the credential file for the given channel ID. Missing
// file is not an error (idempotent). Invalid IDs return an error
// without touching disk.
func (s *SecretStore) Delete(channelID string) error {
	if !channelIDPattern.MatchString(channelID) {
		return fmt.Errorf("comms: secret: invalid channel id %q", channelID)
	}
	path := SecretPath(s.home, channelID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("comms: delete secret %s: %w", path, err)
	}
	return nil
}
