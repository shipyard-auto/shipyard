package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
)

func TestPublishCommands(t *testing.T) {
	p := setup(t)
	seed(t, p) // Família (watched group), Mãe (DM)

	out, _, err := cli(t, "publish", "list")
	if err != nil || !strings.Contains(out, "Nothing is published") {
		t.Fatalf("empty list: %q %v", out, err)
	}
	out, _, err = cli(t, "publish", "add", "watched")
	if err != nil || !strings.Contains(out, "every watched chat") {
		t.Fatalf("add watched: %q %v", out, err)
	}
	out, _, _ = cli(t, "publish", "add", "watched")
	if !strings.Contains(out, "already published") {
		t.Fatalf("re-add: %q", out)
	}
	out, _, err = cli(t, "publish", "add", "mãe")
	if err != nil || !strings.Contains(out, "Mãe") {
		t.Fatalf("add chat by name: %q %v", out, err)
	}
	if _, _, err := cli(t, "publish", "add", "Ninguém"); err == nil {
		t.Fatal("unknown chat accepted")
	}
	cfg, _ := wa.LoadConfig(p.Config)
	if strings.Join(cfg.Publish, ",") != "watched,5511911111111@s.whatsapp.net" {
		t.Fatalf("saved=%v", cfg.Publish)
	}

	out, _, _ = cli(t, "publish", "list")
	if !strings.Contains(out, "every watched chat (1 now)") || !strings.Contains(out, "Mãe") || !strings.Contains(out, "not published") {
		t.Fatalf("list:\n%s", out)
	}
	out, _, _ = cli(t, "publish", "list", "--json")
	var js struct{ Publish []string }
	if err := json.Unmarshal([]byte(out), &js); err != nil || len(js.Publish) != 2 {
		t.Fatalf("json: %s %v", out, err)
	}
	out, _, _ = cli(t, "status")
	if !strings.Contains(out, "publishing 2 selections") {
		t.Fatalf("status:\n%s", out)
	}

	if _, _, err := cli(t, "publish", "rm", "Mãe"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = cli(t, "status")
	if !strings.Contains(out, "publishing watched chats as events") {
		t.Fatalf("status after rm:\n%s", out)
	}
	out, _, _ = cli(t, "publish", "rm", "watched")
	if !strings.Contains(out, "Nothing is published now") {
		t.Fatalf("rm last: %q", out)
	}
	if _, _, err := cli(t, "publish", "add", "*"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = cli(t, "publish", "list")
	if !strings.Contains(out, "every chat (*)") {
		t.Fatalf("list *:\n%s", out)
	}
	if _, _, err := cli(t, "publish", "frobnicate"); err == nil {
		t.Fatal("bad subcommand accepted")
	}
}
