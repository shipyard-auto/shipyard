package crewctl

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func makeResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}
}

func TestResolveLatestCrewVersion_happyPath(t *testing.T) {
	body := `[
		{"tag_name":"v1.1.3","draft":false,"prerelease":false},
		{"tag_name":"fairway-v1.1.3","draft":false,"prerelease":false},
		{"tag_name":"crew-v0.2.4","draft":false,"prerelease":false},
		{"tag_name":"crew-v0.2.3","draft":false,"prerelease":false}
	]`
	client := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.String(), "api.github.com") {
			t.Fatalf("unexpected URL: %s", r.URL)
		}
		return makeResp(200, body), nil
	})
	got, err := ResolveLatestCrewVersion(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "0.2.4" {
		t.Errorf("got %q, want 0.2.4", got)
	}
}

func TestResolveLatestCrewVersion_skipsDraftsAndPrereleases(t *testing.T) {
	body := `[
		{"tag_name":"crew-v0.3.0","draft":true,"prerelease":false},
		{"tag_name":"crew-v0.2.9-rc1","draft":false,"prerelease":true},
		{"tag_name":"crew-v0.2.8","draft":false,"prerelease":false}
	]`
	client := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return makeResp(200, body), nil
	})
	got, err := ResolveLatestCrewVersion(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "0.2.8" {
		t.Errorf("got %q, want 0.2.8", got)
	}
}

func TestResolveLatestCrewVersion_noCrewTagReturnsErrNoCrewRelease(t *testing.T) {
	body := `[{"tag_name":"v1.0.0","draft":false,"prerelease":false}]`
	client := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return makeResp(200, body), nil
	})
	_, err := ResolveLatestCrewVersion(context.Background(), client)
	if !errors.Is(err, ErrNoCrewRelease) {
		t.Errorf("want ErrNoCrewRelease, got %v", err)
	}
}

func TestResolveLatestCrewVersion_nonOKStatus(t *testing.T) {
	client := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 502, Status: "502 Bad Gateway", Body: io.NopCloser(bytes.NewBufferString(""))}, nil
	})
	_, err := ResolveLatestCrewVersion(context.Background(), client)
	if err == nil {
		t.Fatal("expected error for 502")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestResolveLatestCrewVersion_networkError(t *testing.T) {
	boom := errors.New("boom")
	client := roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, boom })
	_, err := ResolveLatestCrewVersion(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected network error wrapping boom, got %v", err)
	}
}
