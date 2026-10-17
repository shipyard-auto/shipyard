package fairway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
	"github.com/shipyard-auto/shipyard/internal/signing"
)

// backend records what an http.forward route delivered to it.
type backend struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  []string
}

func (b *backend) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	b.headers = append(b.headers, r.Header.Clone())
	b.bodies = append(b.bodies, string(raw))
	b.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (b *backend) last(t *testing.T) (http.Header, string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b.mu.Lock()
		n := len(b.headers)
		if n > 0 {
			h, body := b.headers[n-1], b.bodies[n-1]
			b.mu.Unlock()
			return h, body
		}
		b.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("backend never called")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type signedEnv struct {
	ts      *httptest.Server
	clients *fairway.ClientStore
	be      *backend
	secret  string
}

// newSignedEnv serves routes through a real executor against a client
// registry holding "promo" with the given scopes.
func newSignedEnv(t *testing.T, scopes []string, routes ...fairway.Route) *signedEnv {
	t.Helper()
	be := &backend{}
	beSrv := httptest.NewServer(http.HandlerFunc(be.handler))
	t.Cleanup(beSrv.Close)
	for i := range routes {
		if routes[i].Action.Type == fairway.ActionHTTPForward && routes[i].Action.URL == "" {
			routes[i].Action.URL = beSrv.URL
		}
	}
	clients, err := fairway.OpenClientStore(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := clients.Add("promo", scopes)
	if err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig()
	cfg.Routes = routes
	router := fairway.NewRouterWithConfig(&fakeRepo{cfg: cfg}, cfg)
	exec := fairway.NewExecutor(fairway.ExecutorConfig{
		Run: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "env")
		},
	})
	srv := fairway.NewServer(fairway.ServerConfig{Router: router, Executor: exec, Clients: clients})
	ts := httptest.NewServer(serverHandler(srv))
	t.Cleanup(ts.Close)
	return &signedEnv{ts: ts, clients: clients, be: be, secret: c.Secret}
}

func signedRoute(path, scope string, action fairway.Action) fairway.Route {
	return fairway.Route{Path: path, Auth: fairway.Auth{Type: fairway.AuthSigned, Scope: scope}, Action: action}
}

// do sends a request signed by headers h (nil = unsigned) with extra
// headers merged in.
func (e *signedEnv) do(t *testing.T, path string, body []byte, h map[string]string, extra map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (e *signedEnv) sign(path string, body []byte) map[string]string {
	return signing.SignRequest("promo", e.secret, http.MethodPost, path, "", body, time.Now())
}

func TestSignedRouteForwardsIdentity(t *testing.T) {
	e := newSignedEnv(t, []string{"whatsapp.read:120363@g.us"},
		signedRoute("/api/history", "whatsapp.read", fairway.Action{Type: fairway.ActionHTTPForward}))
	body := []byte(`{"chat":"120363@g.us"}`)
	resp := e.do(t, "/api/history", body, e.sign("/api/history", body), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	h, got := e.be.last(t)
	if got != string(body) || h.Get(fairway.HeaderClientID) != "promo" || h.Get(fairway.HeaderClientScopes) != "whatsapp.read:120363@g.us" {
		t.Fatalf("backend saw body=%q id=%q scopes=%q", got, h.Get(fairway.HeaderClientID), h.Get(fairway.HeaderClientScopes))
	}
}

func TestSignedRouteRejections(t *testing.T) {
	e := newSignedEnv(t, []string{"whatsapp.read"},
		signedRoute("/api/history", "whatsapp.read", fairway.Action{Type: fairway.ActionHTTPForward}),
		signedRoute("/api/send", "whatsapp.send", fairway.Action{Type: fairway.ActionHTTPForward}))
	body := []byte(`{}`)

	t.Run("unsigned", func(t *testing.T) {
		if r := e.do(t, "/api/history", body, nil, nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", r.StatusCode)
		}
	})
	t.Run("missing scope is 403", func(t *testing.T) {
		if r := e.do(t, "/api/send", body, e.sign("/api/send", body), nil); r.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d", r.StatusCode)
		}
	})
	t.Run("replay", func(t *testing.T) {
		h := e.sign("/api/history", body)
		if r := e.do(t, "/api/history", body, h, nil); r.StatusCode != http.StatusOK {
			t.Fatalf("first=%d", r.StatusCode)
		}
		if r := e.do(t, "/api/history", body, h, nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("replay=%d", r.StatusCode)
		}
	})
	t.Run("tampered body", func(t *testing.T) {
		h := e.sign("/api/history", body)
		if r := e.do(t, "/api/history", []byte(`{"x":1}`), h, nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", r.StatusCode)
		}
	})
	t.Run("signed for another path", func(t *testing.T) {
		if r := e.do(t, "/api/history", body, e.sign("/api/other", body), nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", r.StatusCode)
		}
	})
	t.Run("stale timestamp", func(t *testing.T) {
		h := signing.SignRequest("promo", e.secret, http.MethodPost, "/api/history", "", body, time.Now().Add(-10*time.Minute))
		if r := e.do(t, "/api/history", body, h, nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", r.StatusCode)
		}
	})
	t.Run("unknown client", func(t *testing.T) {
		h := signing.SignRequest("ghost", e.secret, http.MethodPost, "/api/history", "", body, time.Now())
		if r := e.do(t, "/api/history", body, h, nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", r.StatusCode)
		}
	})
	t.Run("rotated secret stops working", func(t *testing.T) {
		old := e.secret
		c, err := e.clients.Rotate("promo")
		if err != nil {
			t.Fatal(err)
		}
		h := signing.SignRequest("promo", old, http.MethodPost, "/api/history", "", body, time.Now())
		if r := e.do(t, "/api/history", body, h, nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("old secret=%d", r.StatusCode)
		}
		e.secret = c.Secret
		if r := e.do(t, "/api/history", body, e.sign("/api/history", body), nil); r.StatusCode != http.StatusOK {
			t.Fatalf("new secret=%d", r.StatusCode)
		}
	})
	t.Run("disabled client", func(t *testing.T) {
		if err := e.clients.SetDisabled("promo", true); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = e.clients.SetDisabled("promo", false) }()
		if r := e.do(t, "/api/history", body, e.sign("/api/history", body), nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", r.StatusCode)
		}
	})
}

func TestSpoofedIdentityHeadersAreDropped(t *testing.T) {
	e := newSignedEnv(t, []string{"whatsapp.read"},
		fairway.Route{Path: "/open", Auth: fairway.Auth{Type: fairway.AuthLocalOnly}, Action: fairway.Action{Type: fairway.ActionHTTPForward}})
	spoof := map[string]string{fairway.HeaderClientID: "admin", fairway.HeaderClientScopes: "*"}
	if r := e.do(t, "/open", []byte("x"), nil, spoof); r.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", r.StatusCode)
	}
	h, _ := e.be.last(t)
	if h.Get(fairway.HeaderClientID) != "" || h.Get(fairway.HeaderClientScopes) != "" {
		t.Fatalf("spoofed identity reached the service: %v", h)
	}
}

func TestSubprocessActionsGetIdentityEnv(t *testing.T) {
	e := newSignedEnv(t, []string{"cron.run:backup"},
		signedRoute("/jobs/backup", "cron.run:backup", fairway.Action{Type: fairway.ActionCronRun, Target: "backup"}))
	resp := e.do(t, "/jobs/backup", nil, e.sign("/jobs/backup", nil), nil)
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(out), "SHIPYARD_CLIENT_ID=promo") ||
		!strings.Contains(string(out), "SHIPYARD_CLIENT_SCOPES=cron.run:backup") {
		t.Fatalf("status=%d env:\n%s", resp.StatusCode, out)
	}
}

func TestAsyncSignedRouteKeepsIdentity(t *testing.T) {
	r := signedRoute("/api/events", "whatsapp.read", fairway.Action{Type: fairway.ActionHTTPForward})
	r.Async = true
	e := newSignedEnv(t, []string{"whatsapp.read"}, r)
	if resp := e.do(t, "/api/events", []byte("{}"), e.sign("/api/events", []byte("{}")), nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	h, _ := e.be.last(t)
	if h.Get(fairway.HeaderClientID) != "promo" {
		t.Fatalf("async lost the identity: %v", h)
	}
}

func TestSignedRouteWithoutRegistryFailsClosed(t *testing.T) {
	cfg := baseConfig()
	cfg.Routes = []fairway.Route{signedRoute("/api/x", "whatsapp.read", fairway.Action{Type: fairway.ActionHTTPForward, URL: "http://127.0.0.1:1"})}
	router := fairway.NewRouterWithConfig(&fakeRepo{cfg: cfg}, cfg)
	srv := fairway.NewServer(fairway.ServerConfig{Router: router, Executor: &fakeExecutor{}})
	ts := httptest.NewServer(serverHandler(srv))
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/api/x", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d; a signed route without a registry must not let requests through", resp.StatusCode)
	}
}

func TestSignedAuthValidation(t *testing.T) {
	cases := []struct {
		auth fairway.Auth
		ok   bool
	}{
		{fairway.Auth{Type: fairway.AuthSigned, Scope: "whatsapp.read"}, true},
		{fairway.Auth{Type: fairway.AuthSigned, Scope: "cron.run:backup"}, true},
		{fairway.Auth{Type: fairway.AuthSigned}, false},
		{fairway.Auth{Type: fairway.AuthSigned, Scope: "Bad Scope"}, false},
		{fairway.Auth{Type: fairway.AuthSigned, Scope: "x", Token: "t"}, false},
		{fairway.Auth{Type: fairway.AuthLocalOnly, Scope: "x"}, false},
		{fairway.Auth{Type: fairway.AuthBearer, Token: "t", Scope: "x"}, false},
	}
	for _, tc := range cases {
		if err := tc.auth.Validate(); (err == nil) != tc.ok {
			t.Errorf("%+v: err=%v want ok=%v", tc.auth, err, tc.ok)
		}
	}
}

func TestClientStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fw", "clients.json")
	s, err := fairway.OpenClientStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Add("promo-manager", []string{"whatsapp.read", "events.subscribe:whatsapp.message"})
	if err != nil || !strings.HasPrefix(c.Secret, signing.SecretPrefix) {
		t.Fatalf("add: %+v %v", c, err)
	}
	for _, bad := range []struct {
		id     string
		scopes []string
	}{{"Promo", []string{"x"}}, {"p", []string{"x"}}, {"ok-id", nil}, {"ok-id", []string{"Bad Scope"}}, {"promo-manager", []string{"x"}}} {
		if _, err := s.Add(bad.id, bad.scopes); err == nil {
			t.Errorf("Add(%q, %v) accepted", bad.id, bad.scopes)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%o", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), c.Secret) || !strings.Contains(string(raw), "DO NOT EDIT OR SHARE") {
		t.Fatal("secret not persisted with notice")
	}

	// Reopen: data survives; listings never carry secrets.
	s2, err := fairway.OpenClientStore(path)
	if err != nil {
		t.Fatal(err)
	}
	list := s2.List()
	if len(list) != 1 || list[0].ID != "promo-manager" || len(list[0].Scopes) != 2 {
		t.Fatalf("list=%+v", list)
	}
	rot, err := s2.Rotate("promo-manager")
	if err != nil || rot.Secret == c.Secret || rot.RotatedAt.IsZero() {
		t.Fatalf("rotate: %+v %v", rot, err)
	}
	if _, err := s2.SetScopes("promo-manager", []string{"whatsapp.send:120363@g.us"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s2.Lookup("promo-manager")
	if got.Secret != rot.Secret || got.Scopes[0] != "whatsapp.send:120363@g.us" {
		t.Fatalf("after scopes: %+v", got)
	}
	if err := s2.Delete("promo-manager"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Lookup("promo-manager"); ok {
		t.Fatal("deleted client still resolves")
	}
	if err := s2.Delete("promo-manager"); err == nil {
		t.Fatal("double delete accepted")
	}
}

func TestNonceCache(t *testing.T) {
	c := fairway.NewNonceCache()
	now := time.Now()
	if !c.Use("a", now) || c.Use("a", now.Add(time.Minute)) {
		t.Fatal("reuse inside the window must fail")
	}
	if !c.Use("a", now.Add(11*time.Minute)) {
		t.Fatal("a nonce past twice the skew window may be forgotten")
	}
}

func TestSocketClientMethods(t *testing.T) {
	clients, err := fairway.OpenClientStore(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultSocketCfg("v1.2.3", newTestRouter(t), nil)
	cfg.Clients = clients
	sock := startSocket(t, cfg)
	conn, sc := dialAndHandshake(t, sock, "v1.2.3")

	asMap := func(r fairway.Response) map[string]any {
		t.Helper()
		if r.Error != nil {
			t.Fatalf("rpc error: %+v", r.Error)
		}
		m, _ := r.Result.(map[string]any)
		return m
	}
	added := asMap(call(t, conn, sc, 2, "client.add", map[string]any{"id": "promo", "scopes": []string{"whatsapp.read"}}))
	secret, _ := added["secret"].(string)
	if !strings.HasPrefix(secret, signing.SecretPrefix) {
		t.Fatalf("add returned %v", added)
	}
	list := call(t, conn, sc, 3, "client.list", nil)
	if strings.Contains(fmtAny(list.Result), "shps_") {
		t.Fatalf("list leaked a secret: %v", list.Result)
	}
	rotated := asMap(call(t, conn, sc, 4, "client.rotate", map[string]any{"id": "promo"}))
	if rotated["secret"] == secret {
		t.Fatal("rotate kept the secret")
	}
	scoped := asMap(call(t, conn, sc, 5, "client.scopes", map[string]any{"id": "promo", "scopes": []string{"cron.run:backup"}}))
	if fmtAny(scoped["scopes"]) != "[cron.run:backup]" || scoped["secret"] != nil {
		t.Fatalf("scopes=%v", scoped)
	}
	asMap(call(t, conn, sc, 6, "client.disable", map[string]any{"id": "promo"}))
	if c, _ := clients.Lookup("promo"); !c.Disabled {
		t.Fatal("disable had no effect")
	}
	asMap(call(t, conn, sc, 7, "client.delete", map[string]any{"id": "promo"}))
	if r := call(t, conn, sc, 8, "client.delete", map[string]any{"id": "promo"}); r.Error == nil {
		t.Fatal("deleting a missing client succeeded")
	}
	if r := call(t, conn, sc, 9, "client.add", map[string]any{"id": "Bad Id", "scopes": []string{"x"}}); r.Error == nil {
		t.Fatal("invalid id accepted")
	}
}

func fmtAny(v any) string { return strings.TrimSpace(strings.ReplaceAll(fmtV(v), "\n", " ")) }

func fmtV(v any) string {
	switch x := v.(type) {
	case []any:
		parts := make([]string, len(x))
		for i, p := range x {
			parts[i] = fmtV(p)
		}
		return "[" + strings.Join(parts, " ") + "]"
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func TestSignedRejectionsDoNotRevealClients(t *testing.T) {
	e := newSignedEnv(t, []string{"whatsapp.read"},
		signedRoute("/api/history", "whatsapp.read", fairway.Action{Type: fairway.ActionHTTPForward}))
	body := []byte(`{}`)
	read := func(r *http.Response) string { b, _ := io.ReadAll(r.Body); return string(b) }
	unknown := read(e.do(t, "/api/history", body, signing.SignRequest("ghost", "shps_x", "POST", "/api/history", "", body, time.Now()), nil))
	wrong := read(e.do(t, "/api/history", body, signing.SignRequest("promo", "shps_x", "POST", "/api/history", "", body, time.Now()), nil))
	if unknown != wrong {
		t.Fatalf("unknown client %q vs wrong secret %q: responses must be identical", unknown, wrong)
	}
	stale := read(e.do(t, "/api/history", body, signing.SignRequest("ghost", "shps_x", "POST", "/api/history", "", body, time.Now().Add(-time.Hour)), nil))
	if !strings.Contains(stale, "clock") {
		t.Fatalf("stale timestamp should point at the clock: %q", stale)
	}
}
