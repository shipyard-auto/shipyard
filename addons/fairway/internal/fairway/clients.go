package fairway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/shipyard-auto/shipyard/internal/signing"
)

// Client is an external system allowed to call signed routes. Its secret
// signs requests (HMAC, see internal/signing) and is shown once, at
// creation or rotation; it is stored only in clients.json (0600), never in
// routes.json, so route listings cannot leak it.
type Client struct {
	ID        string    `json:"id"`
	Secret    string    `json:"secret"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"createdAt"`
	RotatedAt time.Time `json:"rotatedAt,omitzero"`
	Disabled  bool      `json:"disabled,omitempty"`
}

// ClientInfo is a Client without its secret, for listings.
type ClientInfo struct {
	ID        string    `json:"id"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"createdAt"`
	RotatedAt time.Time `json:"rotatedAt,omitzero"`
	Disabled  bool      `json:"disabled,omitempty"`
}

func (c Client) info() ClientInfo {
	return ClientInfo{ID: c.ID, Scopes: slices.Clone(c.Scopes), CreatedAt: c.CreatedAt, RotatedAt: c.RotatedAt, Disabled: c.Disabled}
}

var clientIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

// Client registry errors.
var (
	ErrInvalidClientID = errors.New("client id must be 2-40 chars: lower-case letters, digits, '-', starting with a letter")
	ErrDuplicateClient = errors.New("client already exists")
	ErrClientNotFound  = errors.New("client not found")
	ErrNoScopes        = errors.New("a client needs at least one scope")
)

const clientsNotice = "DO NOT EDIT OR SHARE THIS FILE. It holds client secrets. Use 'shipyard fairway client' commands."

type clientsFile struct {
	Notice        string   `json:"notice"`
	SchemaVersion string   `json:"schemaVersion"`
	Clients       []Client `json:"clients"`
}

// ClientStore is the in-memory, file-backed client registry. Lookups are
// live: changes apply to the next request without invalidating caches.
type ClientStore struct {
	path    string
	now     func() time.Time
	mu      sync.RWMutex
	clients map[string]Client
}

// DefaultClientsPath is clients.json next to routes.json.
func DefaultClientsPath() (string, error) {
	routes, err := DefaultConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(routes), "clients.json"), nil
}

// OpenClientStore loads path (a missing file is an empty registry).
func OpenClientStore(path string) (*ClientStore, error) {
	s := &ClientStore{path: path, now: time.Now, clients: map[string]Client{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f clientsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, c := range f.Clients {
		s.clients[c.ID] = c
	}
	return s, nil
}

// Lookup returns the client with id.
func (s *ClientStore) Lookup(id string) (Client, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.clients[id]
	return c, ok
}

// List returns every client without secrets, sorted by id.
func (s *ClientStore) List() []ClientInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ClientInfo, 0, len(s.clients))
	for _, c := range s.clients {
		out = append(out, c.info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func validateScopes(scopes []string) error {
	if len(scopes) == 0 {
		return ErrNoScopes
	}
	for _, sc := range scopes {
		if err := signing.ValidateScope(sc); err != nil {
			return err
		}
	}
	return nil
}

// Add registers a client and returns it with its freshly generated secret.
func (s *ClientStore) Add(id string, scopes []string) (Client, error) {
	if !clientIDRe.MatchString(id) {
		return Client{}, ErrInvalidClientID
	}
	if err := validateScopes(scopes); err != nil {
		return Client{}, err
	}
	secret, err := signing.NewSecret()
	if err != nil {
		return Client{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.clients[id]; dup {
		return Client{}, fmt.Errorf("%w: %s", ErrDuplicateClient, id)
	}
	c := Client{ID: id, Secret: secret, Scopes: slices.Clone(scopes), CreatedAt: s.now().UTC()}
	return c, s.commit(func(m map[string]Client) { m[id] = c })
}

// Delete removes a client; its secret stops working immediately.
func (s *ClientStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[id]; !ok {
		return fmt.Errorf("%w: %s", ErrClientNotFound, id)
	}
	return s.commit(func(m map[string]Client) { delete(m, id) })
}

// Rotate replaces a client's secret; the old one stops working at once.
func (s *ClientStore) Rotate(id string) (Client, error) {
	secret, err := signing.NewSecret()
	if err != nil {
		return Client{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	if !ok {
		return Client{}, fmt.Errorf("%w: %s", ErrClientNotFound, id)
	}
	c.Secret, c.RotatedAt = secret, s.now().UTC()
	return c, s.commit(func(m map[string]Client) { m[id] = c })
}

// SetScopes replaces a client's scopes without touching its secret.
func (s *ClientStore) SetScopes(id string, scopes []string) (ClientInfo, error) {
	if err := validateScopes(scopes); err != nil {
		return ClientInfo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	if !ok {
		return ClientInfo{}, fmt.Errorf("%w: %s", ErrClientNotFound, id)
	}
	c.Scopes = slices.Clone(scopes)
	return c.info(), s.commit(func(m map[string]Client) { m[id] = c })
}

// SetDisabled turns a client off (or back on) without deleting it.
func (s *ClientStore) SetDisabled(id string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrClientNotFound, id)
	}
	c.Disabled = disabled
	return s.commit(func(m map[string]Client) { m[id] = c })
}

// commit applies change to a copy, persists it, and only then swaps it in,
// so a failed write leaves memory and disk consistent. Caller holds mu.
func (s *ClientStore) commit(change func(map[string]Client)) error {
	next := make(map[string]Client, len(s.clients)+1)
	for k, v := range s.clients {
		next[k] = v
	}
	change(next)
	f := clientsFile{Notice: clientsNotice, SchemaVersion: "1", Clients: make([]Client, 0, len(next))}
	for _, c := range next {
		f.Clients = append(f.Clients, c)
	}
	sort.Slice(f.Clients, func(i, j int) bool { return f.Clients[i].ID < f.Clients[j].ID })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile0600(s.path, append(data, '\n')); err != nil {
		return err
	}
	s.clients = next
	return nil
}

func writeFile0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".clients.*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
