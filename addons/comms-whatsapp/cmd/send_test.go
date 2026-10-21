package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
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
	media []*wa.MediaMessage
	links []*wa.LinkPreview
}

func (s *stubSender) Upload(_ context.Context, data []byte, _ string) (wa.UploadedMedia, error) {
	return wa.UploadedMedia{FileLength: uint64(len(data))}, nil
}

func (s *stubSender) SendMedia(_ context.Context, _ types.JID, _ wa.UploadedMedia, m *wa.MediaMessage) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.media = append(s.media, m)
	return "MED" + string(rune('0'+len(s.media))), time.Now(), nil
}

func (s *stubSender) SendLink(_ context.Context, _ types.JID, _ string, p *wa.LinkPreview, _ *wa.UploadedMedia) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.links = append(s.links, p)
	return "LNK" + string(rune('0'+len(s.links))), time.Now(), nil
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

func TestAllowGroupNeedsPolicy(t *testing.T) {
	daemonFixture(t)
	if _, _, err := cliIn(t, "", "allow", "Promo"); err == nil || !strings.Contains(err.Error(), "config set allow-groups true") {
		t.Fatalf("allow group with policy off: %v", err)
	}
	if _, _, err := cliIn(t, "", "config", "set", "allow-groups", "true"); err != nil {
		t.Fatal(err)
	}
	if out, _, err := cliIn(t, "", "allow", "Promo"); err != nil || !strings.Contains(out, "may now receive") {
		t.Fatalf("allow group with policy on: %q %v", out, err)
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
	if err != nil || strings.Count(out, "wrote") != 4 {
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

func TestRouteCommands(t *testing.T) {
	p, _ := daemonFixture(t)
	out, _, err := cliIn(t, "", "route", "list")
	if err != nil || !strings.Contains(out, "No routes") || !strings.Contains(out, "127.0.0.1:9876") {
		t.Fatalf("empty list: %q %v", out, err)
	}
	out, _, err = cliIn(t, "", "route", "set", "vini eduqo", "/wa/vini")
	if err != nil || !strings.Contains(out, "Vini Eduqo") || !strings.Contains(out, "shipyard fairway route add --path /wa/vini") {
		t.Fatalf("set: %q %v", out, err)
	}
	if _, _, err := cliIn(t, "", "route", "set", "default", "/wa/inbox"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cliIn(t, "", "route", "set", "Vini Eduqo", "no-slash"); err == nil {
		t.Fatal("invalid path accepted")
	}
	cfg, err := wa.LoadConfig(p.Config)
	if err != nil || cfg.Routes.ByChat[viniJID] != "/wa/vini" || cfg.Routes.Default != "/wa/inbox" {
		t.Fatalf("saved routes=%+v %v", cfg.Routes, err)
	}
	out, _, _ = cliIn(t, "", "route", "list")
	if !strings.Contains(out, "Vini Eduqo") || !strings.Contains(out, "(default)") {
		t.Fatalf("list:\n%s", out)
	}
	out, _, _ = cliIn(t, "", "status")
	if !strings.Contains(out, "2 routes to fairway") {
		t.Fatalf("status:\n%s", out)
	}
	if _, _, err := cliIn(t, "", "route", "rm", "Vini Eduqo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cliIn(t, "", "route", "rm", "Vini Eduqo"); err == nil {
		t.Fatal("removing a missing route must fail")
	}
	if _, _, err := cliIn(t, "", "route", "bogus"); err == nil {
		t.Fatal("bad subcommand accepted")
	}
}

func TestLimitsCommandForPeople(t *testing.T) {
	daemonFixture(t)
	out, _, err := cliIn(t, "", "limits")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"6 per 10 minutes", "20 per hour", "60 per hour", "0 used, 60 left", "sending to groups is OFF", "limits set", "limits reset"} {
		if !strings.Contains(out, want) {
			t.Fatalf("limits missing %q:\n%s", want, out)
		}
	}
	out, _, err = cliIn(t, "", "limits", "set", "per-group", "30/h")
	if err != nil || !strings.Contains(out, "per-group limit is now 30 per hour") {
		t.Fatalf("set: %q %v", out, err)
	}
	if _, _, err := cliIn(t, "", "limits", "set", "per-group", "muitos"); err == nil {
		t.Fatal("bad rate accepted")
	}
	if _, _, err := cliIn(t, "", "limits", "set", "per-planet", "3/h"); err == nil {
		t.Fatal("unknown limit accepted")
	}
	out, _, _ = cliIn(t, "", "limits", "reset")
	if !strings.Contains(out, "restored") || !strings.Contains(out, "20 per hour") {
		t.Fatalf("reset:\n%s", out)
	}

	// Per-chat view lists blockers in plain words.
	out, _, _ = cliIn(t, "", "limits", "Vini Eduqo")
	if !strings.Contains(out, "cannot send now") || !strings.Contains(out, "not_allowed") || !strings.Contains(out, "run 'allow <chat>'") {
		t.Fatalf("chat limits:\n%s", out)
	}
	if _, _, err := cliIn(t, "", "allow", "Vini Eduqo"); err != nil {
		t.Fatal(err)
	}
	if out, _, _ = cliIn(t, "", "limits", "Vini Eduqo"); !strings.Contains(out, "can send now") {
		t.Fatalf("chat limits after allow:\n%s", out)
	}

	// A rate-limited send says when to try again.
	if _, _, err := cliIn(t, "", "limits", "set", "per-chat", "1/10m"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cliIn(t, "", "send", "Vini Eduqo", "--text", "1"); err != nil {
		t.Fatal(err)
	}
	_, _, err = cliIn(t, "", "send", "Vini Eduqo", "--text", "2")
	if exitCode(err) != ExitRefused || !strings.Contains(err.Error(), "rate_limited") || !strings.Contains(err.Error(), "try again in") {
		t.Fatalf("rate-limited send: %v", err)
	}
	env := toolCall(t, "send", `{"chat":"Vini Eduqo","text":"3"}`)
	if d, _ := env["details"].(map[string]any); d["code"] != "rate_limited" || d["retry_after_seconds"] == nil {
		t.Fatalf("tool refusal=%v", env)
	}
	env = toolCall(t, "limits", `{"chat":"Vini Eduqo"}`)
	data, _ := env["data"].(map[string]any)
	chat, _ := data["chat"].(map[string]any)
	if env["ok"] != true || chat["can_send_now"] != false || chat["retry_after_seconds"] == nil {
		t.Fatalf("tool limits=%v", env)
	}
	out, _, _ = cliIn(t, "", "limits", "--json")
	if !strings.Contains(out, `"per_group"`) || !strings.Contains(out, `"human": "60 per hour"`) {
		t.Fatalf("json:\n%s", out)
	}
}

func TestSendFileThroughDaemon(t *testing.T) {
	_, sender := daemonFixture(t)
	if _, _, err := cliIn(t, "", "allow", "Vini Eduqo"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pdf := filepath.Join(dir, "tabela.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cliIn(t, "", "send", "Vini Eduqo", "--voice-note"); err == nil {
		t.Fatal("--voice-note without --file accepted")
	}
	if _, _, err := cliIn(t, "", "send", "Vini Eduqo", "--file", filepath.Join(dir, "missing.pdf")); err == nil {
		t.Fatal("missing file accepted")
	}

	// A relative path works: the CLI resolves it before the daemon reads it.
	t.Chdir(dir)
	out, _, err := cliIn(t, "", "send", "Vini Eduqo", "--file", "tabela.pdf", "--text", "segue a tabela")
	if err != nil || !strings.Contains(out, "Sent a file to Vini Eduqo") {
		t.Fatalf("send file: %q %v", out, err)
	}
	if len(sender.media) != 1 || sender.media[0].Kind != wa.SendKindFile || sender.media[0].FileName != "tabela.pdf" ||
		sender.media[0].Caption != "segue a tabela" || len(sender.texts) != 0 {
		t.Fatalf("media=%+v texts=%v", sender.media, sender.texts)
	}

	// The crew tool takes a file too.
	env := toolCall(t, "send", `{"chat":"Vini Eduqo","file":"`+pdf+`","idempotency_key":"t1"}`)
	if env["ok"] != true || env["data"].(map[string]any)["kind"] != "file" {
		t.Fatalf("tool: %v", env)
	}
	env = toolCall(t, "send", `{"chat":"Vini Eduqo"}`)
	if env["ok"] != false {
		t.Fatalf("tool without text or file: %v", env)
	}
	out, _, _ = cliIn(t, "", "sent")
	if !strings.Contains(out, "KIND") || strings.Count(out, " file ") != 2 {
		t.Fatalf("sent:\n%s", out)
	}
}

func TestSendLinkPreviewThroughDaemon(t *testing.T) {
	_, sender := daemonFixture(t)
	if _, _, err := cliIn(t, "", "allow", "Vini Eduqo"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--text", "x", "--link-title", "sem url"},
		{"--text", "x", "--link-image", "foto.jpg"},
		{"--text", "https://amzn.to/x", "--link-url", "https://amzn.to/x", "--link-image", filepath.Join(t.TempDir(), "missing.jpg")},
	} {
		if _, _, err := cliIn(t, "", append([]string{"send", "Vini Eduqo"}, args...)...); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
	if len(sender.links)+len(sender.texts) != 0 {
		t.Fatalf("sent on a usage error: links=%v texts=%v", sender.links, sender.texts)
	}

	// A relative image path works, as with --file.
	dir := t.TempDir()
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 60, 30))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foto.png"), img.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	out, _, err := cliIn(t, "", "send", "Vini Eduqo", "--text", "Ração 30% off https://amzn.to/x",
		"--link-url", "https://amzn.to/x", "--link-title", "Ração 15kg", "--link-image", "foto.png")
	if err != nil || !strings.Contains(out, "Sent a message to Vini Eduqo") {
		t.Fatalf("send: %q %v", out, err)
	}
	if len(sender.links) != 1 || sender.links[0].Title != "Ração 15kg" || sender.links[0].Width != 60 || len(sender.texts) != 0 {
		t.Fatalf("links=%+v texts=%v", sender.links, sender.texts)
	}
}
