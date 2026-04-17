package fairway

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Authenticator verifies whether an incoming request satisfies a route auth policy.
type Authenticator interface {
	Verify(r *http.Request) error
}

// AuthError represents an authentication or authorization failure suitable for HTTP responses.
type AuthError struct {
	Reason string
	Status int
}

// Error implements the error interface.
func (e *AuthError) Error() string {
	return e.Reason
}

// IsAuth extracts an AuthError from an error chain.
func IsAuth(err error) (*AuthError, bool) {
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return authErr, true
	}
	return nil, false
}

type bearerAuth struct {
	token string
}

type tokenAuth struct {
	value  string
	header string
	query  string
}

type localOnlyAuth struct{}

// NewAuthenticator returns the concrete authenticator for the provided auth policy.
func NewAuthenticator(a Auth) (Authenticator, error) {
	switch a.Type {
	case AuthBearer:
		return bearerAuth{token: a.Token}, nil
	case AuthToken:
		return tokenAuth{
			value:  a.Value,
			header: a.Header,
			query:  a.Query,
		}, nil
	case AuthLocalOnly:
		return localOnlyAuth{}, nil
	default:
		return nil, fmt.Errorf("unknown auth type %q", a.Type)
	}
}

func (a bearerAuth) Verify(r *http.Request) error {
	header := r.Header.Get("Authorization")
	if header == "" {
		return &AuthError{Reason: "missing authorization header", Status: http.StatusUnauthorized}
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" {
		return &AuthError{Reason: "invalid bearer token", Status: http.StatusUnauthorized}
	}

	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(a.token)) != 1 {
		return &AuthError{Reason: "invalid bearer token", Status: http.StatusUnauthorized}
	}

	return nil
}

func (a tokenAuth) Verify(r *http.Request) error {
	var candidate string
	switch {
	case a.header != "":
		candidate = r.Header.Get(a.header)
	case a.query != "":
		candidate = r.URL.Query().Get(a.query)
	default:
		candidate = ""
	}

	if subtle.ConstantTimeCompare([]byte(candidate), []byte(a.value)) != 1 {
		return &AuthError{Reason: "invalid token", Status: http.StatusUnauthorized}
	}

	return nil
}

func (a localOnlyAuth) Verify(r *http.Request) error {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return &AuthError{Reason: "not a local request", Status: http.StatusForbidden}
	}

	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return &AuthError{Reason: "not a local request", Status: http.StatusForbidden}
	}

	return nil
}
