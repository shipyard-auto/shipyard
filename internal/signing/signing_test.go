package signing

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)

// A fixed vector, cross-checked with an independent Python implementation,
// so clients written in other languages can test themselves against it.
func TestKnownVector(t *testing.T) {
	m := Message{Method: "post", Path: "/api/wa/history", RawQuery: "chat=x", Timestamp: 1792267200, Nonce: "abc123", Body: []byte(`{"since":"1h"}`)}
	wantSTS := "v1\nPOST\n/api/wa/history\nchat=x\n1792267200\nabc123\n" +
		"e9b2c9210866d0fd3c1b3f3a4aaf89df4df88f7f9e98fa81b585358528e14c6a"
	if got := m.StringToSign(); got != wantSTS {
		t.Fatalf("string-to-sign:\n%s\nwant:\n%s", got, wantSTS)
	}
	const wantSig = "v1=2d813834e16ac500f0a133ec5270e201d086e40b0f96d7cf8e48db64f9a9b360"
	if got := Sign("shps_secret", m); got != wantSig {
		t.Fatalf("signature=%s want %s", got, wantSig)
	}
}

func TestSignAndCheckRoundTrip(t *testing.T) {
	body := []byte(`{"chat":"x"}`)
	h := SignRequest("promo", "shps_k", "POST", "/v1/send", "", body, now)
	hdr := http.Header{}
	for k, v := range h {
		hdr.Set(k, v)
	}
	p, err := ParseHeaders(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check("shps_k", p, "POST", "/v1/send", "", body, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := []struct {
		name   string
		secret string
		method string
		path   string
		query  string
		body   []byte
		at     time.Time
		want   error
	}{
		{"wrong secret", "shps_x", "POST", "/v1/send", "", body, now, ErrBadSignature},
		{"tampered body", "shps_k", "POST", "/v1/send", "", []byte(`{"chat":"y"}`), now, ErrBadSignature},
		{"other path", "shps_k", "POST", "/v1/other", "", body, now, ErrBadSignature},
		{"added query", "shps_k", "POST", "/v1/send", "a=1", body, now, ErrBadSignature},
		{"other method", "shps_k", "GET", "/v1/send", "", body, now, ErrBadSignature},
		{"too old", "shps_k", "POST", "/v1/send", "", body, now.Add(6 * time.Minute), ErrSkew},
		{"from the future", "shps_k", "POST", "/v1/send", "", body, now.Add(-6 * time.Minute), ErrSkew},
	}
	for _, tc := range cases {
		if err := Check(tc.secret, p, tc.method, tc.path, tc.query, tc.body, tc.at); !errors.Is(err, tc.want) {
			t.Errorf("%s: err=%v want %v", tc.name, err, tc.want)
		}
	}
}

func TestParseHeaders(t *testing.T) {
	if _, err := ParseHeaders(http.Header{}); !errors.Is(err, ErrMissingHeaders) {
		t.Fatalf("empty: %v", err)
	}
	h := http.Header{}
	h.Set(HeaderClient, "c")
	h.Set(HeaderNonce, "n")
	h.Set(HeaderSignature, "v1=x")
	h.Set(HeaderTimestamp, "yesterday")
	if _, err := ParseHeaders(h); !errors.Is(err, ErrBadTimestamp) {
		t.Fatalf("bad ts: %v", err)
	}
}

func TestSecretsAndNonces(t *testing.T) {
	a, err := NewSecret()
	if err != nil || !strings.HasPrefix(a, SecretPrefix) || len(a) != len(SecretPrefix)+64 {
		t.Fatalf("secret=%q err=%v", a, err)
	}
	b, _ := NewSecret()
	if a == b || NewNonce() == NewNonce() {
		t.Fatal("randomness repeated")
	}
}

func TestScopes(t *testing.T) {
	for _, ok := range []string{"whatsapp.read", "whatsapp.read:120363@g.us", "cron.run:backup-diario", "events.subscribe:whatsapp.*", "*", "whatsapp.*"} {
		if err := ValidateScope(ok); err != nil {
			t.Errorf("ValidateScope(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "WhatsApp.read", "a b", "x:", "x:a,b", "x:a b"} {
		if err := ValidateScope(bad); err == nil {
			t.Errorf("ValidateScope(%q) accepted", bad)
		}
	}
	grants := []string{"whatsapp.read:120363@g.us", "cron.run:backup-*", "events.*"}
	cases := map[string]bool{
		"whatsapp.read":                 true,  // resource left to the service
		"whatsapp.read:120363@g.us":     true,  // exact resource
		"whatsapp.read:999@g.us":        false, // other resource
		"whatsapp.send":                 false, // other action
		"cron.run:backup-diario":        true,  // resource glob
		"cron.run:limpeza":              false,
		"events.subscribe:whatsapp.msg": true, // grant without resource covers all
		"eventsXsubscribe":              false,
	}
	for req, want := range cases {
		if got := Grants(grants, req); got != want {
			t.Errorf("Grants(%v, %q)=%v want %v", grants, req, got, want)
		}
	}
	if !Grants([]string{"*"}, "anything.at:all") {
		t.Error("'*' should grant everything")
	}
	if Grants(nil, "whatsapp.read") {
		t.Error("no grants granted something")
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"abc", "abc", true}, {"abc", "abd", false},
		{"a*", "abc", true}, {"*c", "abc", true}, {"a*c", "abbbc", true}, {"a*c", "ab", false},
		{"*", "", true}, {"a*b*c", "axxbyyc", true}, {"a*b*c", "axxcyyb", false},
	}
	for _, tc := range cases {
		if got := Glob(tc.p, tc.s); got != tc.want {
			t.Errorf("Glob(%q,%q)=%v want %v", tc.p, tc.s, got, tc.want)
		}
	}
}
