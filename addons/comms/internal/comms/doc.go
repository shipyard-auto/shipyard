// Package comms holds the domain model for the Shipyard messaging addon:
// the normalized Message envelope, channel configuration types and the
// storage layer that persists them under ~/.shipyard/comms/.
//
// In v1 this package is consumed only by the addon binary
// (addons/comms/cmd). The root shipyard CLI talks to the binary via
// subprocess; it MUST NOT import this package directly (see CLAUDE.md
// architecture rules).
package comms
