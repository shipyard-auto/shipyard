package fairway

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// AuthError is the error type returned by Authenticator.Verify.
// The HTTP handler maps Status to the response status code.
type AuthError struct {
	Reason string
	Status int // 401 for invalid/missing credentials, 403 for forbidden origin
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("auth failed (%d): %s", e.Status, e.Reason)
}

// IsAuthError unwraps err into an *AuthError using errors.As.
// Returns the error and true if it is an *AuthError; nil and false otherwise.
func IsAuthError(err error) (*AuthError, bool) {
	var ae *AuthError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

// Authenticator verifies that an incoming HTTP request is authorised to
// trigger the associated route action.
type Authenticator interface {
	// Verify returns nil if the request passes authentication, or an *AuthError
	// that the HTTP handler can convert to a 401/403 response.
	Verify(r *http.Request) error
}

// NewAuthenticator returns the Authenticator implementation that corresponds
// to the given Auth configuration. Returns an error for unknown AuthType values.
func NewAuthenticator(a Auth) (Authenticator, error) {
	switch a.Type {
	case AuthBearer:
		return &bearerAuth{token: a.Token}, nil
	case AuthToken:
		return &tokenAuth{value: a.Value, header: a.Header, query: a.Query}, nil
	case AuthLocalOnly:
		return &localOnlyAuth{}, nil
	default:
		return nil, fmt.Errorf("unknown auth type %q", a.Type)
	}
}

// ── bearerAuth ────────────────────────────────────────────────────────────────

// bearerAuth validates requests via "Authorization: Bearer <token>".
type bearerAuth struct {
	token string
}

func (b *bearerAuth) Verify(r *http.Request) error {
	hdr := r.Header.Get("Authorization")
	if hdr == "" {
		return &AuthError{Reason: "missing authorization header", Status: 401}
	}

	const prefix = "Bearer "
	if !strings.HasPrefix(hdr, prefix) {
		return &AuthError{Reason: "invalid bearer token", Status: 401}
	}

	incoming := hdr[len(prefix):]
	if incoming == "" {
		return &AuthError{Reason: "invalid bearer token", Status: 401}
	}

	// Constant-time comparison to prevent timing attacks.
	if subtle.ConstantTimeCompare([]byte(incoming), []byte(b.token)) != 1 {
		return &AuthError{Reason: "invalid bearer token", Status: 401}
	}

	return nil
}

// ── tokenAuth ─────────────────────────────────────────────────────────────────

// tokenAuth validates requests by looking for a secret in a header or query
// parameter configured on the route.
type tokenAuth struct {
	value  string
	header string
	query  string
}

func (t *tokenAuth) Verify(r *http.Request) error {
	var incoming string

	if t.header != "" {
		incoming = r.Header.Get(t.header)
	} else if t.query != "" {
		incoming = r.URL.Query().Get(t.query)
	}

	if incoming == "" {
		return &AuthError{Reason: "invalid token", Status: 401}
	}

	// Constant-time comparison to prevent timing attacks.
	if subtle.ConstantTimeCompare([]byte(incoming), []byte(t.value)) != 1 {
		return &AuthError{Reason: "invalid token", Status: 401}
	}

	return nil
}

// ── localOnlyAuth ─────────────────────────────────────────────────────────────

// localOnlyAuth allows only requests originating from loopback addresses.
// X-Forwarded-For is intentionally ignored — only the socket-level RemoteAddr
// is trusted.
type localOnlyAuth struct{}

func (l *localOnlyAuth) Verify(r *http.Request) error {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return &AuthError{Reason: "not a local request", Status: 403}
	}

	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return &AuthError{Reason: "not a local request", Status: 403}
	}

	return nil
}
