package cli

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/signing"
)

type fakeClientAPI struct {
	added    map[string][]string
	disabled map[string]bool
	deleted  []string
	scopes   map[string][]string
}

func newFakeClientAPI() *fakeClientAPI {
	return &fakeClientAPI{added: map[string][]string{}, disabled: map[string]bool{}, scopes: map[string][]string{}}
}

func (f *fakeClientAPI) Close() error { return nil }
func (f *fakeClientAPI) ClientList(context.Context) ([]fairwayctl.ClientInfo, error) {
	var out []fairwayctl.ClientInfo
	for id, sc := range f.added {
		out = append(out, fairwayctl.ClientInfo{ID: id, Scopes: sc, Disabled: f.disabled[id], CreatedAt: time.Now()})
	}
	return out, nil
}
func (f *fakeClientAPI) ClientAdd(_ context.Context, id string, scopes []string) (fairwayctl.IssuedClient, error) {
	f.added[id] = scopes
	return fairwayctl.IssuedClient{ClientInfo: fairwayctl.ClientInfo{ID: id, Scopes: scopes}, Secret: "shps_" + strings.Repeat("a", 64)}, nil
}
func (f *fakeClientAPI) ClientRotate(_ context.Context, id string) (fairwayctl.IssuedClient, error) {
	return fairwayctl.IssuedClient{ClientInfo: fairwayctl.ClientInfo{ID: id}, Secret: "shps_" + strings.Repeat("b", 64)}, nil
}
func (f *fakeClientAPI) ClientSetScopes(_ context.Context, id string, scopes []string) (fairwayctl.ClientInfo, error) {
	f.scopes[id] = scopes
	return fairwayctl.ClientInfo{ID: id, Scopes: scopes}, nil
}
func (f *fakeClientAPI) ClientDelete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *fakeClientAPI) ClientSetDisabled(_ context.Context, id string, d bool) error {
	f.disabled[id] = d
	return nil
}

func runClientCmd(t *testing.T, api *fakeClientAPI, args ...string) (string, error) {
	t.Helper()
	deps := clientDeps{
		route: routeDeps{socketPath: "/nonexistent", version: "test"},
		dial:  func(context.Context, fairwayctl.Opts) (clientAPI, error) { return api, nil },
	}
	cmd := newFairwayClientCmdWith(deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), err
}

func TestClientAddPrintsSecretOnceOrWritesFile(t *testing.T) {
	api := newFakeClientAPI()
	out, err := runClientCmd(t, api, "add", "promo", "--scope", "whatsapp.read", "--scope", "events.subscribe:whatsapp.message")
	if err != nil || !strings.Contains(out, "secret: shps_") || !strings.Contains(out, "not shown again") {
		t.Fatalf("add: %q %v", out, err)
	}
	if got := api.added["promo"]; len(got) != 2 {
		t.Fatalf("scopes sent=%v", got)
	}
	file := filepath.Join(t.TempDir(), "secret")
	out, err = runClientCmd(t, api, "add", "bot", "--scope", "cron.run:backup", "--secret-file", file)
	if err != nil || strings.Contains(out, "shps_") || !strings.Contains(out, file) {
		t.Fatalf("add --secret-file printed the secret or failed: %q %v", out, err)
	}
	info, _ := os.Stat(file)
	raw, _ := os.ReadFile(file)
	if info.Mode().Perm() != 0o600 || !strings.HasPrefix(string(raw), "shps_") {
		t.Fatalf("secret file perm=%o content=%q", info.Mode().Perm(), raw)
	}
	if _, err := runClientCmd(t, api, "add", "noscopes"); err == nil {
		t.Fatal("add without --scope accepted")
	}
}

func TestClientListRotateScopesToggleRemove(t *testing.T) {
	api := newFakeClientAPI()
	api.added["promo"] = []string{"whatsapp.read"}
	out, err := runClientCmd(t, api, "list")
	if err != nil || !strings.Contains(out, "promo") || !strings.Contains(out, "whatsapp.read") || strings.Contains(out, "shps_") {
		t.Fatalf("list: %q %v", out, err)
	}
	if out, _ = runClientCmd(t, api, "rotate", "promo"); !strings.Contains(out, "rotated") || !strings.Contains(out, "shps_b") {
		t.Fatalf("rotate: %q", out)
	}
	if _, err := runClientCmd(t, api, "scopes", "promo", "--scope", "whatsapp.send:120363@g.us"); err != nil || api.scopes["promo"][0] != "whatsapp.send:120363@g.us" {
		t.Fatalf("scopes: %v %v", err, api.scopes)
	}
	if _, err := runClientCmd(t, api, "disable", "promo"); err != nil || !api.disabled["promo"] {
		t.Fatal("disable")
	}
	if out, _ = runClientCmd(t, api, "list"); !strings.Contains(out, "disabled") {
		t.Fatalf("list after disable: %q", out)
	}
	if _, err := runClientCmd(t, api, "enable", "promo"); err != nil || api.disabled["promo"] {
		t.Fatal("enable")
	}
	if _, err := runClientCmd(t, api, "rm", "promo"); err != nil || len(api.deleted) != 1 {
		t.Fatal("rm")
	}
}

func TestClientSignProducesVerifiableHeaders(t *testing.T) {
	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte("shps_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"chat":"Promo"}`
	out, err := runClientCmd(t, newFakeClientAPI(), "sign", "--client", "promo", "--secret-file", file,
		"--url", "http://127.0.0.1:9876/api/wa/history?x=1", "--body", body)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		k, v, _ := strings.Cut(line, ": ")
		h.Set(k, v)
	}
	p, err := signing.ParseHeaders(h)
	if err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if err := signing.Check("shps_test", p, "POST", "/api/wa/history", "x=1", []byte(body), time.Now()); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	out, err = runClientCmd(t, newFakeClientAPI(), "sign", "--client", "promo", "--secret", "shps_test",
		"--url", "http://127.0.0.1:9876/api/x", "--body", `it's`, "--curl")
	if err != nil || !strings.HasPrefix(out, "curl -sS -X POST -H 'X-Shipyard-Client: promo'") || !strings.Contains(out, `'it'\''s'`) {
		t.Fatalf("curl: %q %v", out, err)
	}
	t.Setenv("SHIPYARD_CLIENT_SECRET", "")
	if _, err := runClientCmd(t, newFakeClientAPI(), "sign", "--client", "c", "--url", "http://h/p"); err == nil {
		t.Fatal("sign without a secret succeeded")
	}
}

func TestRouteAddSignedBuildsRoute(t *testing.T) {
	route, err := buildRouteFromAddInput(addRouteInput{
		path: "/api/wa/history", authType: "signed", authScope: "whatsapp.read",
		actionType: "http.forward", url: "http://127.0.0.1:9000/x",
	}, routeDeps{})
	if err != nil || route.Auth.Type != fairwayctl.AuthSigned || route.Auth.Scope != "whatsapp.read" {
		t.Fatalf("route=%+v err=%v", route, err)
	}
	if err := route.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	bad := route
	bad.Auth.Scope = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("signed route without scope validated")
	}
	bad = route
	bad.Auth = fairwayctl.Auth{Type: fairwayctl.AuthLocalOnly, Scope: "x"}
	if err := bad.Validate(); err == nil {
		t.Fatal("scope on a local-only route validated")
	}
}
