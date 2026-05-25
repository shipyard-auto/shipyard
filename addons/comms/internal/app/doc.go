// Package app is the composition root for shipyard-comms. It wires the
// domain types from internal/comms with any future side-effects (HTTP
// clients for providers, credential resolvers). Kept thin on purpose:
// every dependency lives behind an interface defined in internal/comms.
package app
