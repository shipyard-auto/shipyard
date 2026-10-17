package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

const viniJID = "5512982016477@s.whatsapp.net"

type stubSender struct {
	mu    sync.Mutex
	texts []string
}

func (s *stubSender) Connected() bool                               { return true }
func (s *stubSender) Typing(context.Context, types.JID, bool) error { return nil }
func (s *stubSender) SendText(_ context.Context, _ types.JID, text string) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, text)
	return "MSG" + string(rune('0'+len(s.texts))), time.Now(), nil
}

// daemonFixture stands up what `serve` would: a catalog with Vini
// (conversation seen, not yet allowlisted) and the RPC socket backed by an
// Outbox whose transport is a stub. SHIPYARD_HOME lives under /tmp so the
// socket path stays under the unix limit.
func daemonFixture(t *testing.T) (wa.Paths, *stubSender) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "wa-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("SHIPYARD_HOME", home)
	p := wa.PathsAt(filepath.Join(home, "comms", "whatsapp"))
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	arch, err := archive.Open(ctx, p.Archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = arch.Close() })
	for _, c := range []struct {
		jid, name string
		kind      archive.ChatKind
	}{
		{viniJID, "Vini Eduqo", archive.KindDM},
		{"120363@g.us", "Promo", archive.KindGroup},
	} {
		if err := arch.UpsertChat(ctx, c.jid, c.name, c.kind); err != nil {
			t.Fatal(err)
		}
	}
	if err := arch.TouchActivity(ctx, viniJID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	sender := &stubSender{}
	cfgPath := p.Config
	ob := &wa.Outbox{
		Archive: arch,
		Sender:  sender,
		Config:  func() (wa.Config, error) { return wa.LoadConfig(cfgPath) },
		Self:    func() types.JID { return types.NewJID("5535000000000", types.DefaultUserServer) },
		Log:     slog.New(yardlogs.NopHandler()),
		Now:     time.Now,
		Sleep:   func(context.Context, time.Duration) error { return nil },
	}
	ln, err := wa.ListenRPC(p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	srvCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		(&wa.RPCServer{Outbox: ob, Tracker: wa.NewTracker("", 1, nil), Log: ob.Log}).Serve(srvCtx, ln)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return p, sender
}

func cliIn(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	err := run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), err
}

func exitCode(err error) int {
	if e, ok := err.(*wa.ExitError); ok {
		return e.Code
	}
	return -1
}

func TestSendFlowThroughDaemon(t *testing.T) {
	_, sender := daemonFixture(t)

	// Not allowlisted yet → refused with exit code 3.
	_, _, err := cliIn(t, "", "send", "Vini Eduqo", "--text", "oi")
	if exitCode(err) != ExitRefused || !strings.Contains(err.Error(), "not_allowed") {
		t.Fatalf("before allow: %v", err)
	}

	out, _, err := cliIn(t, "", "allow", "vini eduqo")
	if err != nil || !strings.Contains(out, "may now receive") {
		t.Fatalf("allow: %q %v", out, err)
	}

	if _, _, err := cliIn(t, "", "send", "Vini Eduqo", "--dry-run"); err == nil {
		t.Fatal("send without text must fail")
	}
	out, _, err = cliIn(t, "", "send", "+55 12 98201-6477", "--text", "oi", "--dry-run")
	if err != nil || !strings.Contains(out, "would be accepted") || len(sender.texts) != 0 {
		t.Fatalf("dry run: %q %v", out, err)
	}

	out, _, err = cliIn(t, "linha 1\nlinha 2\n", "send", "Vini Eduqo", "--stdin", "--idempotency-key", "k1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res wa.SendResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.MsgID == "" || res.ChatName != "Vini Eduqo" {
		t.Fatalf("send json: %q %v", out, err)
	}
	if len(sender.texts) != 1 || sender.texts[0] != "linha 1\nlinha 2" {
		t.Fatalf("sent=%q", sender.texts)
	}
	out, _, err = cliIn(t, "", "send", "Vini Eduqo", "--text", "outra coisa", "--idempotency-key", "k1")
	if err != nil || !strings.Contains(out, "not sent again") || len(sender.texts) != 1 {
		t.Fatalf("idempotent replay: %q %v sent=%d", out, err, len(sender.texts))
	}

	out, _, err = cliIn(t, "", "pause")
	if err != nil || !strings.Contains(out, "paused") {
		t.Fatalf("pause: %q %v", out, err)
	}
	if _, _, err := cliIn(t, "", "send", "Vini Eduqo", "--text", "oi"); !strings.Contains(err.Error(), "paused") {
		t.Fatalf("send while paused: %v", err)
	}
	out, _, _ = cliIn(t, "", "status")
	if !strings.Contains(out, "PAUSED") || !strings.Contains(out, "1 sent in the last hour") {
		t.Fatalf("status while paused:\n%s", out)
	}
	if _, _, err := cliIn(t, "", "resume"); err != nil {
		t.Fatal(err)
	}

	out, _, err = cliIn(t, "", "deny", "Vini Eduqo")
	if err != nil || !strings.Contains(out, "removed") {
		t.Fatalf("deny: %q %v", out, err)
	}
}

func TestAllowRefusesGroups(t *testing.T) {
	daemonFixture(t)
	if _, _, err := cliIn(t, "", "allow", "Promo"); err == nil || !strings.Contains(err.Error(), "groups cannot") {
		t.Fatalf("allow group: %v", err)
	}
}

func TestSendWithoutDaemon(t *testing.T) {
	p := setup(t)
	seed(t, p)
	_, _, err := cliIn(t, "", "send", "Mãe", "--text", "oi")
	if err == nil || !strings.Contains(err.Error(), "not running") || exitCode(err) == ExitRefused {
		t.Fatalf("err=%v", err)
	}
}

func toolCall(t *testing.T, name, input string) map[string]any {
	t.Helper()
	out, _, err := cliIn(t, input, "tool", name)
	if err != nil {
		t.Fatalf("tool %s exited with error: %v", name, err)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("tool %s output not JSON: %q", name, out)
	}
	return env
}

func TestCrewToolsSpeakTheEnvelope(t *testing.T) {
	_, sender := daemonFixture(t)

	env := toolCall(t, "send", `{"chat":"Vini Eduqo","text":"oi do agente"}`)
	if env["ok"] != false || env["details"].(map[string]any)["code"] != "not_allowed" {
		t.Fatalf("refusal envelope=%v", env)
	}
	if _, _, err := cliIn(t, "", "allow", "Vini Eduqo"); err != nil {
		t.Fatal(err)
	}
	env = toolCall(t, "send", `{"chat":"Vini Eduqo","text":"oi do agente","idempotency_key":"run-1"}`)
	if env["ok"] != true || env["data"].(map[string]any)["chat_name"] != "Vini Eduqo" || len(sender.texts) != 1 {
		t.Fatalf("send envelope=%v", env)
	}
	if env := toolCall(t, "send", `{"chat":"Vini Eduqo"}`); env["ok"] != false {
		t.Fatalf("missing text=%v", env)
	}
	if env := toolCall(t, "send", `not json`); env["ok"] != false {
		t.Fatalf("bad json=%v", env)
	}

	env = toolCall(t, "chats", `{"search":"vini"}`)
	chats := env["data"].(map[string]any)["chats"].([]any)
	if env["ok"] != true || len(chats) != 1 || chats[0].(map[string]any)["send_allowed"] != true {
		t.Fatalf("chats envelope=%v", env)
	}

	if env := toolCall(t, "history", `{"chat":"Promo"}`); env["ok"] != false || !strings.Contains(env["error"].(string), "not watched") {
		t.Fatalf("history of unwatched=%v", env)
	}
	if env := toolCall(t, "nope", `{}`); env["ok"] != false {
		t.Fatalf("unknown tool=%v", env)
	}
}

func TestToolsInstallWritesDefinitions(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	out, _, err := cliIn(t, "", "tools", "install", "--dir", dir)
	if err != nil || strings.Count(out, "wrote") != 3 {
		t.Fatalf("install: %q %v", out, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "wa_send.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	y := string(raw)
	for _, want := range []string{"name: wa_send", "protocol: exec", "  - tool\n  - send\n", "  chat: string", "  idempotency_key: string"} {
		if !strings.Contains(y, want) {
			t.Fatalf("wa_send.yaml missing %q:\n%s", want, y)
		}
	}
	if _, _, err := cliIn(t, "", "tools", "install", "--dir", dir); err == nil {
		t.Fatal("second install without --force must refuse to overwrite")
	}
	if _, _, err := cliIn(t, "", "tools", "install", "--dir", dir, "--force"); err != nil {
		t.Fatalf("--force: %v", err)
	}
}
