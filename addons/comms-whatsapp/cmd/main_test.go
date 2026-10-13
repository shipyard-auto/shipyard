package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

// cli runs one command against an isolated SHIPYARD_HOME.
func cli(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	err := run(context.Background(), args, &out, &errb)
	return out.String(), errb.String(), err
}

func setup(t *testing.T) wa.Paths {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SHIPYARD_HOME", home)
	return wa.PathsAt(filepath.Join(home, "comms", "whatsapp"))
}

// seed adds chats and messages straight into the archive, standing in for
// what the daemon would have written.
func seed(t *testing.T, p wa.Paths) {
	t.Helper()
	ctx := context.Background()
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	a, err := archive.Open(ctx, p.Archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	for _, c := range []struct {
		jid, name string
		kind      archive.ChatKind
	}{
		{"120363@g.us", "Família", archive.KindGroup},
		{"5511911111111@s.whatsapp.net", "Mãe", archive.KindDM},
	} {
		if err := a.UpsertChat(ctx, c.jid, c.name, c.kind); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Watch(ctx, "120363@g.us", 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, m := range []archive.Message{
		{ID: "1", SenderJID: "5511911111111@s.whatsapp.net", SenderName: "Mãe", Kind: "text", Text: "almoço domingo?"},
		{ID: "2", SenderJID: "5511900000000@s.whatsapp.net", FromMe: true, Kind: "text", Text: "vou sim"},
		{ID: "3", SenderJID: "5511933333333@s.whatsapp.net", Kind: "image", Text: "foto do bolo"},
	} {
		m.ChatJID = "120363@g.us"
		m.Timestamp = now.Add(time.Duration(i-3) * time.Minute)
		if _, err := a.InsertMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVersionAndHelp(t *testing.T) {
	setup(t)
	out, _, err := cli(t, "--version")
	if err != nil || strings.TrimSpace(out) != "dev" {
		t.Fatalf("version: %q %v", out, err)
	}
	out, _, err = cli(t)
	if err != nil || !strings.Contains(out, "watch <chat>") {
		t.Fatalf("help: %q %v", out, err)
	}
	if _, _, err := cli(t, "nope"); err == nil {
		t.Fatal("unknown command must fail")
	}
}

func TestChatsWatchHistoryUnwatch(t *testing.T) {
	p := setup(t)
	seed(t, p)

	out, _, err := cli(t, "chats", "--search", "familia")
	if err != nil || !strings.Contains(out, "Família") || strings.Contains(out, "Mãe") {
		t.Fatalf("chats search: %q %v", out, err)
	}

	// Flags after the positional argument must still parse.
	out, _, err = cli(t, "watch", "mae", "--ttl", "3d")
	if err != nil || !strings.Contains(out, "Watching Mãe") || !strings.Contains(out, "3d") {
		t.Fatalf("watch: %q %v", out, err)
	}
	out, _, err = cli(t, "chats", "--watched", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Name string `json:"name"`
		TTL  string `json:"ttl"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("chats json: %q %v", out, err)
	}

	out, _, err = cli(t, "history", "Família", "--since", "1h")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 ||
		!strings.HasSuffix(lines[0], "Mãe: almoço domingo?") ||
		!strings.HasSuffix(lines[1], "me: vou sim") ||
		!strings.HasSuffix(lines[2], "5511933333333: [image] foto do bolo") {
		t.Fatalf("history:\n%s", out)
	}
	out, _, err = cli(t, "history", "120363@g.us", "--limit", "1", "--json")
	if err != nil || !strings.Contains(out, "foto do bolo") || strings.Contains(out, "vou sim") {
		t.Fatalf("history json limit: %q %v", out, err)
	}

	out, _, err = cli(t, "unwatch", "Família")
	if err != nil || !strings.Contains(out, "deleted 3") {
		t.Fatalf("unwatch: %q %v", out, err)
	}
	_, stderr, err := cli(t, "history", "Família")
	if err != nil || !strings.Contains(stderr, "not watched") {
		t.Fatalf("history after unwatch: %q %v", stderr, err)
	}
}

func TestWatchByPhoneNumberAndErrors(t *testing.T) {
	setup(t)
	out, _, err := cli(t, "watch", "+55 (11) 98888-7777")
	if err != nil || !strings.Contains(out, "added by number") {
		t.Fatalf("watch by phone: %q %v", out, err)
	}
	if _, _, err := cli(t, "history", "5511988887777"); err != nil {
		t.Fatalf("history by phone: %v", err)
	}
	if _, _, err := cli(t, "watch", "Ninguém"); err == nil {
		t.Fatal("unknown name must fail")
	}
	if _, _, err := cli(t, "watch", "x", "--ttl", "500d"); err == nil {
		t.Fatal("out-of-range ttl must fail")
	}
	if _, _, err := cli(t, "watch"); err == nil {
		t.Fatal("missing chat must fail")
	}
}

func TestConfigAndStatus(t *testing.T) {
	setup(t)
	out, _, err := cli(t, "config", "set", "default-ttl", "7d")
	if err != nil || !strings.Contains(out, "default-ttl = 7d") {
		t.Fatalf("config set: %q %v", out, err)
	}
	out, _, err = cli(t, "config")
	if err != nil || !strings.Contains(out, `"default_ttl": "7d"`) {
		t.Fatalf("config show: %q %v", out, err)
	}
	if _, _, err := cli(t, "config", "set", "nope", "x"); err == nil {
		t.Fatal("unknown key must fail")
	}
	out, _, err = cli(t, "status")
	if err != nil || !strings.Contains(out, "Paired:") || !strings.Contains(out, "never started") || !strings.Contains(out, "7d") {
		t.Fatalf("status: %q %v", out, err)
	}
	out, _, err = cli(t, "status", "--json")
	if err != nil || !strings.Contains(out, `"paired": false`) {
		t.Fatalf("status json: %q %v", out, err)
	}
}

func TestParseInterspersed(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	ttl := fs.String("ttl", "", "")
	pos, err := parse(fs, []string{"a", "--ttl", "3d", "b"})
	if err != nil || *ttl != "3d" || strings.Join(pos, ",") != "a,b" {
		t.Fatalf("pos=%v ttl=%q err=%v", pos, *ttl, err)
	}
}

func TestPhoneJID(t *testing.T) {
	ok := map[string]string{
		"+55 (11) 98888-7777": "5511988887777@s.whatsapp.net",
		"5511988887777":       "5511988887777@s.whatsapp.net",
	}
	for in, want := range ok {
		if got, valid := phoneJID(in); !valid || got != want {
			t.Errorf("phoneJID(%q)=%q,%v", in, got, valid)
		}
	}
	for _, in := range []string{"Mãe", "011988887777", "1234", "55119888877771234"} {
		if _, valid := phoneJID(in); valid {
			t.Errorf("phoneJID(%q) must be invalid", in)
		}
	}
}
