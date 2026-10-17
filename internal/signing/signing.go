// Package signing implements Shipyard's request-signing scheme (v1), shared
// by every party of the protocol: fairway verifies signed requests, the
// core CLI signs them (`shipyard fairway client sign`), and addons sign the
// webhooks they deliver. External systems implement the same few lines in
// their own language; the algorithm is deliberately small.
//
// A signed request carries four headers:
//
//	X-Shipyard-Client     the client id
//	X-Shipyard-Timestamp  unix seconds when it was signed
//	X-Shipyard-Nonce      a random value, never reused
//	X-Shipyard-Signature  "v1=" + hex(HMAC-SHA256(secret, string-to-sign))
//
// string-to-sign joins, with "\n":
//
//	"v1", METHOD (upper case), path, raw query ("" if none), timestamp,
//	nonce, hex(SHA-256(body))
//
// The verifier rejects unknown or disabled clients, timestamps more than
// MaxSkew away from its clock, reused nonces and signature mismatches
// (constant-time compare). The secret is the full secret string as bytes.
package signing

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Header names.
const (
	HeaderClient    = "X-Shipyard-Client"
	HeaderTimestamp = "X-Shipyard-Timestamp"
	HeaderNonce     = "X-Shipyard-Nonce"
	HeaderSignature = "X-Shipyard-Signature"
)

// Version is the scheme identifier, first line of the string-to-sign and
// prefix of the signature header value.
const Version = "v1"

// MaxSkew is how far a timestamp may be from the verifier's clock.
const MaxSkew = 5 * time.Minute

// SecretPrefix marks Shipyard client secrets so leaked ones are easy to
// recognise in logs and scanners.
const SecretPrefix = "shps_"

// NewSecret returns a fresh client secret: SecretPrefix + 64 hex chars.
func NewSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("signing: generate secret: %w", err)
	}
	return SecretPrefix + hex.EncodeToString(b[:]), nil
}

// NewNonce returns a random 128-bit nonce in hex.
func NewNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Message is what gets signed.
type Message struct {
	Method    string
	Path      string
	RawQuery  string
	Timestamp int64
	Nonce     string
	Body      []byte
}

// StringToSign renders the canonical string for m.
func (m Message) StringToSign() string {
	sum := sha256.Sum256(m.Body)
	return strings.Join([]string{
		Version,
		strings.ToUpper(m.Method),
		m.Path,
		m.RawQuery,
		strconv.FormatInt(m.Timestamp, 10),
		m.Nonce,
		hex.EncodeToString(sum[:]),
	}, "\n")
}

// Sign returns the signature header value for m: "v1=<hex>".
func Sign(secret string, m Message) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(m.StringToSign()))
	return Version + "=" + hex.EncodeToString(mac.Sum(nil))
}

// SignRequest fills a fresh timestamp and nonce, signs, and returns the
// four headers to send.
func SignRequest(clientID, secret, method, path, rawQuery string, body []byte, now time.Time) map[string]string {
	m := Message{Method: method, Path: path, RawQuery: rawQuery, Timestamp: now.Unix(), Nonce: NewNonce(), Body: body}
	return map[string]string{
		HeaderClient:    clientID,
		HeaderTimestamp: strconv.FormatInt(m.Timestamp, 10),
		HeaderNonce:     m.Nonce,
		HeaderSignature: Sign(secret, m),
	}
}

// Verification failures. Callers map them to 401 without telling the
// requester which check failed beyond the message.
var (
	ErrMissingHeaders = errors.New("missing signature headers")
	ErrBadTimestamp   = errors.New("timestamp is not a unix time")
	ErrSkew           = errors.New("timestamp outside the allowed window")
	ErrBadSignature   = errors.New("signature mismatch")
)

// Parsed holds the signature headers of a request.
type Parsed struct {
	ClientID  string
	Timestamp int64
	Nonce     string
	Signature string
}

// ParseHeaders extracts the signature headers from h.
func ParseHeaders(h http.Header) (Parsed, error) {
	p := Parsed{
		ClientID:  strings.TrimSpace(h.Get(HeaderClient)),
		Nonce:     strings.TrimSpace(h.Get(HeaderNonce)),
		Signature: strings.TrimSpace(h.Get(HeaderSignature)),
	}
	ts := strings.TrimSpace(h.Get(HeaderTimestamp))
	if p.ClientID == "" || p.Nonce == "" || p.Signature == "" || ts == "" {
		return Parsed{}, ErrMissingHeaders
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return Parsed{}, ErrBadTimestamp
	}
	p.Timestamp = n
	return p, nil
}

// CheckSkew reports whether ts (unix seconds) is within MaxSkew of now.
// It needs no secret, so verifiers run it before looking the client up.
func CheckSkew(ts int64, now time.Time) error {
	if d := now.Sub(time.Unix(ts, 0)); d > MaxSkew || d < -MaxSkew {
		return ErrSkew
	}
	return nil
}

// Check verifies the timestamp window and the signature of a parsed
// request against secret. Nonce replay is the caller's job (it needs
// state).
func Check(secret string, p Parsed, method, path, rawQuery string, body []byte, now time.Time) error {
	if err := CheckSkew(p.Timestamp, now); err != nil {
		return err
	}
	want := Sign(secret, Message{Method: method, Path: path, RawQuery: rawQuery, Timestamp: p.Timestamp, Nonce: p.Nonce, Body: body})
	if !hmac.Equal([]byte(want), []byte(p.Signature)) {
		return ErrBadSignature
	}
	return nil
}
