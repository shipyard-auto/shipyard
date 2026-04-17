package fairway_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func assertPass(t *testing.T, auth fairway.Authenticator, r *http.Request) {
	t.Helper()
	if err := auth.Verify(r); err != nil {
		t.Errorf("expected pass, got error: %v", err)
	}
}

func assertFail(t *testing.T, auth fairway.Authenticator, r *http.Request, wantStatus int) {
	t.Helper()
	err := auth.Verify(r)
	if err == nil {
		t.Fatal("expected auth failure, got nil error")
	}
	ae, ok := fairway.IsAuthError(err)
	if !ok {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
	if ae.Status != wantStatus {
		t.Errorf("AuthError.Status = %d; want %d", ae.Status, wantStatus)
	}
}

func newBearerAuth(token string) fairway.Authenticator {
	a, _ := fairway.NewAuthenticator(fairway.Auth{Type: fairway.AuthBearer, Token: token})
	return a
}

func newTokenHeaderAuth(value, header string) fairway.Authenticator {
	a, _ := fairway.NewAuthenticator(fairway.Auth{Type: fairway.AuthToken, Value: value, Header: header})
	return a
}

func newTokenQueryAuth(value, query string) fairway.Authenticator {
	a, _ := fairway.NewAuthenticator(fairway.Auth{Type: fairway.AuthToken, Value: value, Query: query})
	return a
}

func newLocalOnlyAuth() fairway.Authenticator {
	a, _ := fairway.NewAuthenticator(fairway.Auth{Type: fairway.AuthLocalOnly})
	return a
}

func reqWithRemoteAddr(method, target, remoteAddr string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = remoteAddr
	return r
}

// ── Bearer ────────────────────────────────────────────────────────────────────

func TestBearer_validToken_passes(t *testing.T) {
	t.Parallel()
	auth := newBearerAuth("supersecret")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer supersecret")
	assertPass(t, auth, r)
}

func TestBearer_missingHeader_401(t *testing.T) {
	t.Parallel()
	auth := newBearerAuth("tok")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	assertFail(t, auth, r, 401)
}

func TestBearer_wrongScheme_lowercase_401(t *testing.T) {
	t.Parallel()
	auth := newBearerAuth("tok")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "bearer tok") // lowercase 'b'
	assertFail(t, auth, r, 401)
}

func TestBearer_wrongScheme_tokenPrefix_401(t *testing.T) {
	t.Parallel()
	auth := newBearerAuth("tok")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Token tok")
	assertFail(t, auth, r, 401)
}

func TestBearer_invalidToken_401(t *testing.T) {
	t.Parallel()
	auth := newBearerAuth("correct")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer wrong")
	assertFail(t, auth, r, 401)
}

func TestBearer_emptyToken_401(t *testing.T) {
	t.Parallel()
	auth := newBearerAuth("tok")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer ") // space only, empty value
	assertFail(t, auth, r, 401)
}

// ── Token (header) ────────────────────────────────────────────────────────────

func TestToken_headerMatch_passes(t *testing.T) {
	t.Parallel()
	auth := newTokenHeaderAuth("secret", "X-Webhook-Token")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Webhook-Token", "secret")
	assertPass(t, auth, r)
}

func TestToken_headerMismatch_401(t *testing.T) {
	t.Parallel()
	auth := newTokenHeaderAuth("secret", "X-Webhook-Token")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Webhook-Token", "wrong")
	assertFail(t, auth, r, 401)
}

func TestToken_headerAbsent_401(t *testing.T) {
	t.Parallel()
	auth := newTokenHeaderAuth("secret", "X-Webhook-Token")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	assertFail(t, auth, r, 401)
}

// ── Token (query) ─────────────────────────────────────────────────────────────

func TestToken_queryMatch_passes(t *testing.T) {
	t.Parallel()
	auth := newTokenQueryAuth("secret", "token")
	r := httptest.NewRequest(http.MethodPost, "/?token=secret", nil)
	assertPass(t, auth, r)
}

func TestToken_queryMismatch_401(t *testing.T) {
	t.Parallel()
	auth := newTokenQueryAuth("secret", "token")
	r := httptest.NewRequest(http.MethodPost, "/?token=wrong", nil)
	assertFail(t, auth, r, 401)
}

func TestToken_queryAbsent_401(t *testing.T) {
	t.Parallel()
	auth := newTokenQueryAuth("secret", "token")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	assertFail(t, auth, r, 401)
}

// ── LocalOnly ─────────────────────────────────────────────────────────────────

func TestLocalOnly_loopbackIPv4_passes(t *testing.T) {
	t.Parallel()
	auth := newLocalOnlyAuth()
	r := reqWithRemoteAddr(http.MethodPost, "/", "127.0.0.1:12345")
	assertPass(t, auth, r)
}

func TestLocalOnly_loopbackIPv6_passes(t *testing.T) {
	t.Parallel()
	auth := newLocalOnlyAuth()
	r := reqWithRemoteAddr(http.MethodPost, "/", "[::1]:12345")
	assertPass(t, auth, r)
}

func TestLocalOnly_publicIP_403(t *testing.T) {
	t.Parallel()
	auth := newLocalOnlyAuth()
	r := reqWithRemoteAddr(http.MethodPost, "/", "8.8.8.8:12345")
	assertFail(t, auth, r, 403)
}

func TestLocalOnly_privateIP_403(t *testing.T) {
	t.Parallel()
	auth := newLocalOnlyAuth()
	r := reqWithRemoteAddr(http.MethodPost, "/", "10.0.0.5:12345")
	assertFail(t, auth, r, 403)
}

func TestLocalOnly_forwardedForIgnored_403(t *testing.T) {
	t.Parallel()
	auth := newLocalOnlyAuth()
	// RemoteAddr is a public IP; X-Forwarded-For claims loopback — must be ignored.
	r := reqWithRemoteAddr(http.MethodPost, "/", "8.8.8.8:12345")
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	assertFail(t, auth, r, 403)
}

func TestLocalOnly_malformedRemoteAddr_403(t *testing.T) {
	t.Parallel()
	auth := newLocalOnlyAuth()
	r := reqWithRemoteAddr(http.MethodPost, "/", "not-an-addr")
	assertFail(t, auth, r, 403)
}

// ── Factory ───────────────────────────────────────────────────────────────────

func TestNewAuthenticator_bearer_returnsBearerImpl(t *testing.T) {
	t.Parallel()
	auth, err := fairway.NewAuthenticator(fairway.Auth{Type: fairway.AuthBearer, Token: "t"})
	if err != nil {
		t.Fatalf("NewAuthenticator() error: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil Authenticator")
	}
	// Verify it behaves as bearer: missing header → 401.
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	assertFail(t, auth, r, 401)
}

func TestNewAuthenticator_token_returnsTokenImpl(t *testing.T) {
	t.Parallel()
	auth, err := fairway.NewAuthenticator(fairway.Auth{Type: fairway.AuthToken, Value: "v", Header: "X-H"})
	if err != nil {
		t.Fatalf("NewAuthenticator() error: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil Authenticator")
	}
}

func TestNewAuthenticator_localOnly_returnsLocalOnlyImpl(t *testing.T) {
	t.Parallel()
	auth, err := fairway.NewAuthenticator(fairway.Auth{Type: fairway.AuthLocalOnly})
	if err != nil {
		t.Fatalf("NewAuthenticator() error: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil Authenticator")
	}
}

func TestNewAuthenticator_unknownType_returnsError(t *testing.T) {
	t.Parallel()
	_, err := fairway.NewAuthenticator(fairway.Auth{Type: "magic"})
	if err == nil {
		t.Fatal("NewAuthenticator() expected error for unknown type")
	}
}

// ── AuthError ─────────────────────────────────────────────────────────────────

func TestAuthError_errorString(t *testing.T) {
	t.Parallel()
	ae := &fairway.AuthError{Reason: "bad token", Status: 401}
	s := ae.Error()
	if s == "" {
		t.Error("AuthError.Error() returned empty string")
	}
}

func TestIsAuthError_wrappedError(t *testing.T) {
	t.Parallel()
	orig := &fairway.AuthError{Reason: "nope", Status: 403}
	wrapped := fmt.Errorf("middleware: %w", orig)

	ae, ok := fairway.IsAuthError(wrapped)
	if !ok {
		t.Fatal("IsAuthError() expected true for wrapped *AuthError")
	}
	if ae.Status != 403 {
		t.Errorf("unwrapped Status = %d; want 403", ae.Status)
	}
}

func TestIsAuthError_nonAuthError_returnsFalse(t *testing.T) {
	t.Parallel()
	_, ok := fairway.IsAuthError(errors.New("plain error"))
	if ok {
		t.Error("IsAuthError() should return false for non-*AuthError")
	}
}
