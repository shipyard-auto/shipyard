package fairway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBearerAuth(t *testing.T) {
	auth := bearerAuth{token: "secret"}

	tests := []struct {
		name       string
		header     string
		wantReason string
		wantStatus int
	}{
		{name: "Bearer_validToken_passes", header: "Bearer secret"},
		{name: "Bearer_missingHeader_401", wantReason: "missing authorization header", wantStatus: http.StatusUnauthorized},
		{name: "Bearer_wrongSchemeToken_401", header: "Token secret", wantReason: "invalid bearer token", wantStatus: http.StatusUnauthorized},
		{name: "Bearer_wrongSchemeLowercase_401", header: "bearer secret", wantReason: "invalid bearer token", wantStatus: http.StatusUnauthorized},
		{name: "Bearer_invalidToken_401", header: "Bearer nope", wantReason: "invalid bearer token", wantStatus: http.StatusUnauthorized},
		{name: "Bearer_emptyToken_401", header: "Bearer ", wantReason: "invalid bearer token", wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/hooks", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			err := auth.Verify(req)
			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("Verify() error = %v", err)
				}
				return
			}

			authErr, ok := IsAuth(err)
			if !ok {
				t.Fatalf("Verify() error = %v, want AuthError", err)
			}
			if authErr.Reason != tt.wantReason || authErr.Status != tt.wantStatus {
				t.Fatalf("Verify() authErr = %#v, want reason=%q status=%d", authErr, tt.wantReason, tt.wantStatus)
			}
		})
	}
}

func TestTokenAuth(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		auth := tokenAuth{value: "secret", header: "X-Token"}
		tests := []struct {
			name       string
			header     string
			wantReason string
		}{
			{name: "Token_headerMatch_passes", header: "secret"},
			{name: "Token_headerMismatch_401", header: "nope", wantReason: "invalid token"},
			{name: "Token_headerAbsent_401", wantReason: "invalid token"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "http://example.com/hooks", nil)
				if tt.header != "" {
					req.Header.Set("X-Token", tt.header)
				}

				err := auth.Verify(req)
				if tt.wantReason == "" {
					if err != nil {
						t.Fatalf("Verify() error = %v", err)
					}
					return
				}

				authErr, ok := IsAuth(err)
				if !ok {
					t.Fatalf("Verify() error = %v, want AuthError", err)
				}
				if authErr.Reason != tt.wantReason || authErr.Status != http.StatusUnauthorized {
					t.Fatalf("Verify() authErr = %#v, want invalid token 401", authErr)
				}
			})
		}
	})

	t.Run("query", func(t *testing.T) {
		auth := tokenAuth{value: "secret", query: "token"}
		tests := []struct {
			name       string
			query      string
			wantReason string
		}{
			{name: "Token_queryMatch_passes", query: "secret"},
			{name: "Token_queryMismatch_401", query: "nope", wantReason: "invalid token"},
			{name: "Token_queryAbsent_401", wantReason: "invalid token"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				target := "http://example.com/hooks"
				if tt.query != "" {
					target += "?token=" + tt.query
				}
				req := httptest.NewRequest(http.MethodGet, target, nil)

				err := auth.Verify(req)
				if tt.wantReason == "" {
					if err != nil {
						t.Fatalf("Verify() error = %v", err)
					}
					return
				}

				authErr, ok := IsAuth(err)
				if !ok {
					t.Fatalf("Verify() error = %v, want AuthError", err)
				}
				if authErr.Reason != tt.wantReason || authErr.Status != http.StatusUnauthorized {
					t.Fatalf("Verify() authErr = %#v, want invalid token 401", authErr)
				}
			})
		}
	})
}

func TestLocalOnlyAuth(t *testing.T) {
	auth := localOnlyAuth{}

	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		wantReason string
	}{
		{name: "LocalOnly_loopbackIPv4_passes", remoteAddr: "127.0.0.1:12345"},
		{name: "LocalOnly_loopbackIPv6_passes", remoteAddr: "[::1]:12345"},
		{name: "LocalOnly_publicIP_403", remoteAddr: "8.8.8.8:12345", wantReason: "not a local request"},
		{name: "LocalOnly_privateIP_403", remoteAddr: "10.0.0.5:12345", wantReason: "not a local request"},
		{name: "LocalOnly_forwardedForIgnored_403", remoteAddr: "8.8.8.8:12345", forwarded: "127.0.0.1", wantReason: "not a local request"},
		{name: "LocalOnly_malformedRemoteAddr_403", remoteAddr: "bad-remote", wantReason: "not a local request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/hooks", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tt.forwarded)
			}

			err := auth.Verify(req)
			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("Verify() error = %v", err)
				}
				return
			}

			authErr, ok := IsAuth(err)
			if !ok {
				t.Fatalf("Verify() error = %v, want AuthError", err)
			}
			if authErr.Reason != tt.wantReason || authErr.Status != http.StatusForbidden {
				t.Fatalf("Verify() authErr = %#v, want forbidden local-only error", authErr)
			}
		})
	}
}

func TestNewAuthenticator(t *testing.T) {
	tests := []struct {
		name    string
		auth    Auth
		wantNil bool
		wantErr bool
	}{
		{name: "NewAuthenticator_bearer_returnsBearerImpl", auth: Auth{Type: AuthBearer, Token: "secret"}},
		{name: "NewAuthenticator_token_returnsTokenImpl", auth: Auth{Type: AuthToken, Value: "secret", Header: "X-Token"}},
		{name: "NewAuthenticator_localOnly_returnsLocalOnlyImpl", auth: Auth{Type: AuthLocalOnly}},
		{name: "NewAuthenticator_unknownType_returnsError", auth: Auth{Type: AuthType("unknown")}, wantNil: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewAuthenticator(tt.auth)
			if tt.wantErr {
				if err == nil {
					t.Fatal("NewAuthenticator() error = nil, want error")
				}
				if got != nil {
					t.Fatalf("NewAuthenticator() authenticator = %#v, want nil", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewAuthenticator() error = %v", err)
			}
			if got == nil {
				t.Fatal("NewAuthenticator() authenticator = nil, want impl")
			}
		})
	}
}

func TestIsAuth(t *testing.T) {
	err := &AuthError{Reason: "invalid token", Status: http.StatusUnauthorized}
	got, ok := IsAuth(err)
	if !ok {
		t.Fatal("IsAuth() ok = false, want true")
	}
	if got != err {
		t.Fatalf("IsAuth() = %#v, want same pointer %#v", got, err)
	}
}

func TestAuthUsesConstantTimeCompare(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "auth.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}

	source := string(data)
	for _, fragment := range []string{
		"subtle.ConstantTimeCompare([]byte(parts[1]), []byte(a.token))",
		"subtle.ConstantTimeCompare([]byte(candidate), []byte(a.value))",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("auth.go does not contain %q", fragment)
		}
	}
}
