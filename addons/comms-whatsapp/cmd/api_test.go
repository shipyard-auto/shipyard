package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
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
	for name, path := range map[string]string{"messages": wa.APIMessagesPath, "chats": wa.APIChatsPath} {
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
		if r.Path != "/api/whatsapp/"+name || r.Auth.Type != "signed" || r.Auth.Scope != "whatsapp.read" ||
			r.Action.Type != "http.forward" || r.Action.URL != "http://"+wa.DefaultAPIAddr+path || r.Action.Method != "POST" ||
			r.Action.Headers["Authorization"] != "Bearer "+token || r.Timeout != 30*time.Second {
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
