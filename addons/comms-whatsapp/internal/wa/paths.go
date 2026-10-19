// Package wa is the domain layer of shipyard-comms-whatsapp: on-disk
// layout, configuration, daemon status, the linked-device session and the
// event handling that feeds the local archive.
package wa

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths is the on-disk layout under ~/.shipyard/comms/whatsapp/.
type Paths struct {
	Dir     string // root, 0700
	Config  string // config.json
	Session string // session.db — whatsmeow keys and device state
	Archive string // archive.db — archived messages of watched chats
	RunDir  string // run/, 0700
	Status  string // run/status.json — written by the daemon
	Lock    string // run/wa.lock — held by whichever process owns the session
	Socket  string // run/wa.sock — the daemon's control socket (send, status)
	// APIToken is the query API's shared secret with fairway (0600), and
	// APIRoutes the directory of generated fairway route definitions.
	APIToken  string
	APIRoutes string
	// Uploads holds API uploads in progress (0700); emptied at start.
	Uploads string
	// FairwayDir is the sibling fairway addon's state, read (never
	// written) to find where to deliver inbound events.
	FairwayDir string
}

// Home returns the whatsapp addon root: $SHIPYARD_HOME/comms/whatsapp when
// SHIPYARD_HOME is set, otherwise ~/.shipyard/comms/whatsapp. Mirrors
// comms.CommsHome; duplicated because addons must not import each other's
// internals.
func Home() (string, error) {
	if h := os.Getenv("SHIPYARD_HOME"); h != "" {
		return filepath.Join(h, "comms", "whatsapp"), nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("whatsapp: resolve home dir: %w", err)
	}
	return filepath.Join(h, ".shipyard", "comms", "whatsapp"), nil
}

// PathsAt returns the layout rooted at dir.
func PathsAt(dir string) Paths {
	run := filepath.Join(dir, "run")
	return Paths{
		Dir:       dir,
		Config:    filepath.Join(dir, "config.json"),
		Session:   filepath.Join(dir, "session.db"),
		Archive:   filepath.Join(dir, "archive.db"),
		RunDir:    run,
		Status:    filepath.Join(run, "status.json"),
		Lock:      filepath.Join(run, "wa.lock"),
		Socket:    filepath.Join(run, "wa.sock"),
		APIToken:  filepath.Join(dir, "api.token"),
		APIRoutes: filepath.Join(dir, "api-routes"),
		Uploads:   filepath.Join(dir, "uploads"),
		// dir is <shipyard home>/comms/whatsapp.
		FairwayDir: filepath.Join(filepath.Dir(filepath.Dir(dir)), "fairway"),
	}
}

// DefaultPaths resolves Home and returns its layout.
func DefaultPaths() (Paths, error) {
	dir, err := Home()
	if err != nil {
		return Paths{}, err
	}
	return PathsAt(dir), nil
}

// Ensure creates the root and run directories with mode 0700. Message
// content lives here, so the directories are never group/world readable.
func (p Paths) Ensure() error {
	for _, d := range []string{p.Dir, p.RunDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("whatsapp: create %s: %w", d, err)
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return fmt.Errorf("whatsapp: chmod %s: %w", d, err)
		}
	}
	return nil
}
