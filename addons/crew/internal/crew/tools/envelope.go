// Package tools implements the shared contract and drivers used to invoke
// tools declared in agent.yaml. This file defines the JSON envelope every
// driver must return to the dispatcher.
package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Envelope is the fixed JSON contract returned by every tool invocation.
// Success produces {ok:true, data:<raw>}; failure produces
// {ok:false, error:<msg>, details:<raw>}. Data and Details are preserved as
// raw JSON so client-side typing survives the transport.
type Envelope struct {
	Ok      bool            `json:"ok"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
}

// Success builds an {ok:true} envelope wrapping data. A nil data produces an
// envelope without the "data" field. If data cannot be marshalled (e.g.
// channels), a Failure envelope is returned instead — this is a defensive
// fallback that should not occur for values produced by the dispatcher.
func Success(data any) Envelope {
	e := Envelope{Ok: true}
	if data == nil {
		return e
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return Failure("envelope: marshal data failed: "+err.Error(), nil)
	}
	e.Data = raw
	return e
}

// Failure builds an {ok:false} envelope with the given human message and
// optional details. Callers must pass a non-empty msg — an empty message
// produces an envelope that Parse will reject.
func Failure(msg string, details any) Envelope {
	e := Envelope{Ok: false, Error: msg}
	if details == nil {
		return e
	}
	raw, err := json.Marshal(details)
	if err != nil {
		e.Error = e.Error + " (details unserialized: " + err.Error() + ")"
		return e
	}
	e.Details = raw
	return e
}

// Parse validates and decodes a tool envelope. It enforces:
//   - non-empty payload
//   - valid JSON object
//   - presence of the "ok" field
//   - if ok=false, a non-empty "error" message
//
// Extra fields are accepted and ignored (stdlib default).
func Parse(raw []byte) (Envelope, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Envelope{}, errors.New("envelope: empty payload")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Envelope{}, fmt.Errorf("envelope: invalid json: %w", err)
	}
	if _, hasOk := probe["ok"]; !hasOk {
		return Envelope{}, errors.New("envelope: missing required field \"ok\"")
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, fmt.Errorf("envelope: decode struct: %w", err)
	}
	if !env.Ok && env.Error == "" {
		return Envelope{}, errors.New("envelope: ok=false requires error message")
	}
	return env, nil
}
