package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

func TestAPICommands(t *testing.T) {
	p := setup(t)
	out, _, err := cli(t, "api")
	if err != nil || !strings.Contains(out, "off") {
		t.Fatalf("status off: %q %v", out, err)
	}
	if _, _, err := cli(t, "api", "enable", "--addr", "0.0.0.0:9877"); err == nil {
		t.Fatal("non-loopback addr accepted")
	}
	if _, _, err := cli(t, "api", "enable", "--prefix", "/_shipyard/wa"); err == nil {
		t.Fatal("reserved prefix accepted")
	}
	out, _, err = cli(t, "api", "enable")
	if err != nil || !strings.Contains(out, "Restart the daemon") ||
		!strings.Contains(out, "shipyard fairway route add --from-file "+filepath.Join(p.APIRoutes, "messages.json")) {
		t.Fatalf("enable: %q %v", out, err)
	}
	cfg, _ := wa.LoadConfig(p.Config)
	token, _ := wa.ReadAPIToken(p.APIToken)
	if cfg.APIAddr != wa.DefaultAPIAddr || token == "" {
		t.Fatalf("addr=%q token=%q", cfg.APIAddr, token)
	}

	// The generated files are valid fairway route definitions.
	for name, want := range map[string]struct {
		path, scope string
		timeout     time.Duration
	}{
		"messages": {wa.APIMessagesPath, "whatsapp.read", 30 * time.Second},
		"chats":    {wa.APIChatsPath, "whatsapp.read", 30 * time.Second},
		"send":     {wa.APISendPath, "whatsapp.send", 60 * time.Second},
		"limits":   {wa.APILimitsPath, "whatsapp.send", 30 * time.Second},
		"upload":   {wa.APIUploadPath, "whatsapp.send", 60 * time.Second},
	} {
		file := filepath.Join(p.APIRoutes, name+".json")
		st, err := os.Stat(file)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", file, st, err)
		}
		raw, _ := os.ReadFile(file)
		var r fairwayRoute
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		if r.Path != "/api/whatsapp/"+name || r.Auth.Type != "signed" || r.Auth.Scope != want.scope ||
			r.Action.Type != "http.forward" || r.Action.URL != "http://"+wa.DefaultAPIAddr+want.path || r.Action.Method != "POST" ||
			r.Action.Headers["Authorization"] != "Bearer "+token || r.Timeout != want.timeout {
			t.Fatalf("%s route=%+v", name, r)
		}
	}

	// Enabling again keeps the token and does not ask for a restart.
	out, _, _ = cli(t, "api", "enable")
	if again, _ := wa.ReadAPIToken(p.APIToken); again != token || strings.Contains(out, "Restart") {
		t.Fatalf("re-enable changed token or asked restart: %q", out)
	}
	out, _, _ = cli(t, "api")
	if !strings.Contains(out, "not serving") {
		t.Fatalf("status without daemon: %q", out)
	}
	out, _, _ = cli(t, "status")
	if !strings.Contains(out, "Query API:") {
		t.Fatalf("status line missing:\n%s", out)
	}

	out, _, err = cli(t, "api", "rotate-token")
	rotated, _ := wa.ReadAPIToken(p.APIToken)
	raw, _ := os.ReadFile(filepath.Join(p.APIRoutes, "chats.json"))
	if err != nil || rotated == token || !strings.Contains(string(raw), rotated) || !strings.Contains(out, "route delete /api/whatsapp/chats") {
		t.Fatalf("rotate: %q %v", out, err)
	}

	if _, _, err := cli(t, "api", "disable"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = wa.LoadConfig(p.Config)
	if cfg.APIAddr != "" {
		t.Fatal("disable kept addr")
	}
	if _, _, err := cli(t, "api", "rotate-token"); err == nil {
		t.Fatal("rotate while off must fail")
	}
}

func TestSentCommand(t *testing.T) {
	p := setup(t)
	seed(t, p)
	out, _, err := cli(t, "sent")
	if err != nil || !strings.Contains(out, "Nothing sent") {
		t.Fatalf("empty: %q %v", out, err)
	}
	ctx := context.Background()
	a, err := archive.Open(ctx, p.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.UpsertChat(ctx, viniJID, "Vini Eduqo", archive.KindDM); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, s := range []archive.Sent{
		{MsgID: "M1", ChatJID: viniJID, Timestamp: now.Add(-time.Hour), Chars: 10},
		{MsgID: "M2", ChatJID: viniJID, Timestamp: now.Add(-time.Minute), Chars: 20, Client: "promo-manager", IdempotencyKey: "promo-manager/p1"},
		{MsgID: "M3", ChatJID: viniJID, Timestamp: now.Add(-72 * time.Hour), Chars: 30},
	} {
		if err := a.RecordSent(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	_ = a.Close()
	out, _, err = cli(t, "sent")
	if err != nil || !strings.Contains(out, "promo-manager") || !strings.Contains(out, "local") || strings.Count(out, "Vini Eduqo") != 2 {
		t.Fatalf("list:\n%s %v", out, err)
	}
	if strings.Index(out, "promo-manager") > strings.Index(out, "local") {
		t.Fatalf("not newest first:\n%s", out)
	}
	out, _, _ = cli(t, "sent", "--client", "promo-manager", "--json")
	var rows []archive.SentEntry
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0].MsgID != "M2" || rows[0].ChatName != "Vini Eduqo" {
		t.Fatalf("json: %s %v", out, err)
	}
	out, _, _ = cli(t, "sent", "--since", "7d", "--client", "local")
	if strings.Count(out, "Vini Eduqo") != 2 || strings.Contains(out, "promo-manager") {
		t.Fatalf("local 7d:\n%s", out)
	}
}
