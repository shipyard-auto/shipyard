package addon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/shipyard-auto/shipyard/internal/crewctl"
	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
)

// RegistrySchemaVersion is the on-disk schema version for addons.json.
// Unknown versions cause Load to refuse the file rather than misinterpret it.
const RegistrySchemaVersion = 1

// registryFileName is the file under ~/.shipyard/ that holds the cached
// state of every known addon.
const registryFileName = "addons.json"

// Info is a snapshot describing a single addon's presence on disk.
// An Info with Installed=false means the binary could not be located at
// detection time; other fields are zero in that case.
type Info struct {
	Kind       Kind      `json:"kind"`
	Installed  bool      `json:"installed"`
	BinaryPath string    `json:"binaryPath,omitempty"`
	Version    string    `json:"version,omitempty"`
	LastCheck  time.Time `json:"lastCheck,omitempty"`
}

// File is the serialized form of ~/.shipyard/addons.json.
type File struct {
	SchemaVersion int             `json:"schemaVersion"`
	Addons        map[Kind]*Info  `json:"addons"`
	WrittenAt     time.Time       `json:"writtenAt"`
	// Unknown holds fields we did not recognise so round-tripping is lossless.
	Unknown map[string]json.RawMessage `json:"-"`
}

// Detector resolves a Kind to its on-disk state (binary path, version).
// The default Detector delegates to crewctl and fairwayctl resolvers.
type Detector interface {
	Detect(kind Kind) (Info, error)
}

// ErrUnknownKind is returned when a Kind is not handled by the registry.
var ErrUnknownKind = errors.New("addon: unknown kind")

// ErrUnsupportedSchema is returned by Load when addons.json uses an
// unexpected schema version.
var ErrUnsupportedSchema = errors.New("addon: unsupported registry schema version")

// defaultDetector uses the real ctl packages; tests substitute their own.
type defaultDetector struct{}

func (defaultDetector) Detect(kind Kind) (Info, error) {
	switch kind {
	case KindCrew:
		path, err := crewctl.ResolveBinary()
		if err != nil {
			if errors.Is(err, crewctl.ErrNotInstalled) {
				return Info{Kind: kind, Installed: false, LastCheck: time.Now()}, nil
			}
			return Info{}, fmt.Errorf("addon: detect crew: %w", err)
		}
		return Info{Kind: kind, Installed: true, BinaryPath: path, LastCheck: time.Now()}, nil
	case KindFairway:
		path, err := fairwayctl.ResolveBinary()
		if err != nil {
			if errors.Is(err, fairwayctl.ErrNotInstalled) {
				return Info{Kind: kind, Installed: false, LastCheck: time.Now()}, nil
			}
			return Info{}, fmt.Errorf("addon: detect fairway: %w", err)
		}
		return Info{Kind: kind, Installed: true, BinaryPath: path, LastCheck: time.Now()}, nil
	default:
		return Info{}, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
}

// Registry resolves and persists per-addon state to ~/.shipyard/addons.json.
// Zero-value Registry is valid and uses the default detector, resolves Home
// via os.UserHomeDir and writes to <home>/.shipyard/addons.json.
type Registry struct {
	// Home overrides the shipyard state root (defaults to ~/.shipyard). Used by
	// tests and by callers that manage a non-default location.
	Home string

	// Detector overrides the default on-disk detection (used by tests).
	Detector Detector

	// Now returns the current time (used by tests for deterministic output).
	Now func() time.Time
}

// NewRegistry returns a Registry with home set and defaults filled in. Pass
// an empty string to use ~/.shipyard.
func NewRegistry(home string) *Registry {
	return &Registry{Home: home}
}

func (r *Registry) detector() Detector {
	if r.Detector != nil {
		return r.Detector
	}
	return defaultDetector{}
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Root returns the absolute path of the shipyard state directory
// (default: ~/.shipyard). It never creates the directory.
func (r *Registry) Root() (string, error) {
	if r.Home != "" {
		return r.Home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("addon: resolve home: %w", err)
	}
	return filepath.Join(home, ".shipyard"), nil
}

// path returns the full registry file path.
func (r *Registry) path() (string, error) {
	root, err := r.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, registryFileName), nil
}

// Detect performs a live lookup for a single addon without touching the
// on-disk registry file.
func (r *Registry) Detect(kind Kind) (Info, error) {
	return r.detector().Detect(kind)
}

// Load reads addons.json from disk. When the file does not exist, Load
// returns an empty File with the current schema version rather than an
// error — callers can treat "never written" and "empty" uniformly.
func (r *Registry) Load() (*File, error) {
	p, err := r.path()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &File{SchemaVersion: RegistrySchemaVersion, Addons: map[Kind]*Info{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("addon: read %s: %w", p, err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("addon: parse %s: %w", p, err)
	}
	if f.SchemaVersion != RegistrySchemaVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSchema, f.SchemaVersion, RegistrySchemaVersion)
	}
	if f.Addons == nil {
		f.Addons = map[Kind]*Info{}
	}
	return &f, nil
}

// Save writes addons.json atomically (temp file + rename) with mode 0600.
func (r *Registry) Save(f *File) error {
	if f == nil {
		return errors.New("addon: nil file")
	}
	if f.SchemaVersion == 0 {
		f.SchemaVersion = RegistrySchemaVersion
	}
	f.WrittenAt = r.now().UTC()

	// stable ordering for deterministic output
	sorted := map[Kind]*Info{}
	keys := make([]Kind, 0, len(f.Addons))
	for k := range f.Addons {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		sorted[k] = f.Addons[k]
	}
	f.Addons = sorted

	root, err := r.Root()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("addon: create state dir: %w", err)
	}

	p, err := r.path()
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("addon: marshal: %w", err)
	}

	tmp, err := os.CreateTemp(root, ".addons-*.json")
	if err != nil {
		return fmt.Errorf("addon: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("addon: write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("addon: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("addon: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, p); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("addon: rename: %w", err)
	}
	return nil
}

// Record upserts a single addon entry and persists the registry file.
// Version is optional — callers that want the recorded version can pass it
// explicitly (e.g. right after a successful install).
func (r *Registry) Record(kind Kind, installed bool, binaryPath, version string) error {
	f, err := r.Load()
	if err != nil {
		// corrupted file: start fresh rather than block install flows
		f = &File{SchemaVersion: RegistrySchemaVersion, Addons: map[Kind]*Info{}}
	}
	f.Addons[kind] = &Info{
		Kind:       kind,
		Installed:  installed,
		BinaryPath: binaryPath,
		Version:    version,
		LastCheck:  r.now().UTC(),
	}
	return r.Save(f)
}

// Forget removes an addon entry (idempotent).
func (r *Registry) Forget(kind Kind) error {
	f, err := r.Load()
	if err != nil {
		// nothing to clean up if the file is unreadable
		return nil //nolint:nilerr // best effort
	}
	if _, ok := f.Addons[kind]; !ok {
		return nil
	}
	delete(f.Addons, kind)
	return r.Save(f)
}
