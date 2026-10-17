package fairway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shipyard-auto/shipyard/internal/signing"
)

// Identity is the authenticated client of a signed request. It travels in
// the request context to the action, which hands it to the service behind
// the route (env vars for subprocess actions, headers for http.forward).
// The service narrows by resource; fairway never interprets resources.
type Identity struct {
	ClientID string
	Scopes   []string
}

// Headers and env vars carrying the identity to the service.
const (
	HeaderClientID     = "X-Shipyard-Client-Id"
	HeaderClientScopes = "X-Shipyard-Client-Scopes"
	EnvClientID        = "SHIPYARD_CLIENT_ID"
	EnvClientScopes    = "SHIPYARD_CLIENT_SCOPES"
)

type identityKey struct{}

// WithIdentity returns ctx carrying id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the identity of an authenticated signed request.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// ContextAuthenticator is an Authenticator that also attaches what it
// learned (the client identity) to the request.
type ContextAuthenticator interface {
	VerifyContext(r *http.Request) (*http.Request, error)
}

// ClientLookup resolves client ids; *ClientStore implements it.
type ClientLookup interface {
	Lookup(id string) (Client, bool)
}

// signedAuth verifies Shipyard v1 signatures (internal/signing) and that
// the client holds the route's required scope.
type signedAuth struct {
	clients ClientLookup
	scope   string
	nonces  *NonceCache
	now     func() time.Time
}

func (a *signedAuth) Verify(r *http.Request) error {
	_, err := a.VerifyContext(r)
	return err
}

func (a *signedAuth) VerifyContext(r *http.Request) (*http.Request, error) {
	p, err := signing.ParseHeaders(r.Header)
	if err != nil {
		return nil, &AuthError{Reason: "signed request required: " + err.Error(), Status: 401}
	}
	// The body is part of the signature, so read it here and put it back
	// for the action.
	var body []byte
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, MaxSubprocessOutput+1))
		if err != nil {
			return nil, &AuthError{Reason: "cannot read body", Status: 400}
		}
		if int64(len(body)) > MaxSubprocessOutput {
			return nil, &AuthError{Reason: "request body too large", Status: 413}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	// The clock check needs no client, so it runs first and its message can
	// be specific (clock drift is the most common integration bug). After
	// that, unknown, disabled and mis-signed requests get one identical
	// answer, so responses never reveal which client ids exist.
	if err := signing.CheckSkew(p.Timestamp, a.now()); err != nil {
		return nil, &AuthError{Reason: "timestamp outside the allowed window (check the client's clock)", Status: 401}
	}
	c, ok := a.clients.Lookup(p.ClientID)
	if !ok || c.Disabled {
		return nil, &AuthError{Reason: "invalid signature", Status: 401}
	}
	if err := signing.Check(c.Secret, p, r.Method, r.URL.Path, r.URL.RawQuery, body, a.now()); err != nil {
		return nil, &AuthError{Reason: "invalid signature", Status: 401}
	}
	// Record the nonce only after the signature checks out, so
	// unauthenticated traffic cannot fill the cache.
	if !a.nonces.Use(p.ClientID+"\x00"+p.Nonce, a.now()) {
		return nil, &AuthError{Reason: "replayed request (nonce already used)", Status: 401}
	}
	// An empty scope means "any authenticated client"; used only by
	// fairway's own API, which then authorises per operation.
	if a.scope != "" && !signing.Grants(c.Scopes, a.scope) {
		return nil, &AuthError{Reason: "client lacks scope " + a.scope, Status: 403}
	}
	return r.WithContext(WithIdentity(r.Context(), Identity{ClientID: c.ID, Scopes: c.Scopes})), nil
}

// NonceCache remembers nonces for twice the signing window: a request
// older than that is rejected by its timestamp anyway, so forgetting the
// nonce then is safe. In memory: a daemon restart forgets nonces, but
// replays still need a timestamp inside the window.
type NonceCache struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	ttl     time.Duration
	lastGC  time.Time
	maxSize int
}

// NewNonceCache returns a cache sized for the signing window.
func NewNonceCache() *NonceCache {
	return &NonceCache{seen: map[string]time.Time{}, ttl: 2 * signing.MaxSkew, maxSize: 100_000}
}

// Use records key and reports whether it was fresh.
func (c *NonceCache) Use(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.lastGC) > time.Minute || len(c.seen) >= c.maxSize {
		for k, t := range c.seen {
			if now.Sub(t) > c.ttl {
				delete(c.seen, k)
			}
		}
		c.lastGC = now
	}
	if t, ok := c.seen[key]; ok && now.Sub(t) <= c.ttl {
		return false
	}
	if len(c.seen) >= c.maxSize {
		// Under a flood of valid, unique nonces: refuse rather than grow.
		return false
	}
	c.seen[key] = now
	return true
}

// identityEnv renders the identity as env vars for subprocess actions.
func identityEnv(id Identity) []string {
	return []string{EnvClientID + "=" + id.ClientID, EnvClientScopes + "=" + strings.Join(id.Scopes, ",")}
}
