// Package addon centralises detection of optional Shipyard addons
// (currently crew and fairway). It is the single source of truth used by
// Cobra PreRunE middleware and by interactive wizards that need to ask
// "is this addon available right now?" without duplicating lookup logic.
package addon

import "fmt"

// Kind identifies an optional addon.
type Kind string

const (
	// KindCrew is the shipyard-crew addon (agent runtime).
	KindCrew Kind = "crew"

	// KindFairway is the shipyard-fairway addon (HTTP gateway).
	KindFairway Kind = "fairway"
)

// String returns the Kind name as used in CLI commands (e.g. "crew").
func (k Kind) String() string { return string(k) }

// BinaryName returns the on-disk binary name for the addon (e.g. "shipyard-crew").
func (k Kind) BinaryName() string { return fmt.Sprintf("shipyard-%s", k) }

// InstallCommand returns the CLI string the user should run to install
// this addon (e.g. "shipyard crew install").
func (k Kind) InstallCommand() string { return fmt.Sprintf("shipyard %s install", k) }

// AllKinds returns every known addon kind in a stable order.
func AllKinds() []Kind { return []Kind{KindCrew, KindFairway} }
