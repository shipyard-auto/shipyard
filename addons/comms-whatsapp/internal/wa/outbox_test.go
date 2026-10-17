package wa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

var (
	viniJID = types.NewJID("5512982016477", types.DefaultUserServer)
	t0Send  = time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)
)

type fakeSender struct {
	mu        sync.Mutex
	connected bool
	fail      error
	calls     []string // "typing:on", "typing:off", "send:<text>"
	n         int
}

func (f *fakeSender) Connected() bool { return f.connected }

func (f *fakeSender) Typing(_ context.Context, _ types.JID, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("typing:%v", on))
	return nil
}

func (f *fakeSender) SendText(_ context.Context, _ types.JID, text string) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return "", time.Time{}, f.fail
	}
	f.n++
	f.calls = append(f.calls, "send:"+text)
	return fmt.Sprintf("MSG%d", f.n), t0Send, nil
}

func (f *fakeSender) sends() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

type outboxHarness struct {
	ob     *Outbox
	arch   *archive.Store
	sender *fakeSender
	cfg    Config
	now    time.Time
	slept  []time.Duration
}

// newOutbox builds an Outbox over a fresh archive with Vini catalogued,
// allowlisted and with prior activity — the happy path. Tests subtract
// from that.
func newOutbox(t *testing.T) *outboxHarness {
	t.Helper()
	arch, err := archive.Open(context.Background(), filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = arch.Close() })
	h := &outboxHarness{arch: arch, sender: &fakeSender{connected: true}, cfg: DefaultConfig(), now: t0Send}
	arch.WithClock(func() time.Time { return h.now })
	ctx := context.Background()
	mustDo(t, arch.UpsertChat(ctx, viniJID.String(), "Vini Eduqo", archive.KindDM))
	mustDo(t, arch.SetSendAllowed(ctx, viniJID.String(), true))
	mustDo(t, arch.TouchActivity(ctx, viniJID.String(), t0Send.Add(-time.Hour)))
	h.ob = &Outbox{
		Archive: arch,
		Sender:  h.sender,
		Config:  func() (Config, error) { return h.cfg, nil },
		Self:    func() types.JID { return ownPN },
		Log:     slog.New(yardlogs.NopHandler()),
		Now:     func() time.Time { return h.now },
		Sleep: func(_ context.Context, d time.Duration) error {
			h.slept = append(h.slept, d)
			return nil
		},
	}
	return h
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (h *outboxHarness) send(text string) (SendResult, error) {
	return h.ob.Send(context.Background(), SendRequest{Chat: viniJID.String(), Text: text})
}

func codeOf(err error) SendCode {
	var se *SendError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func TestSendHappyPathRecordsAndHumanizes(t *testing.T) {
	h := newOutbox(t)
	res, err := h.send("oi Vini, teste do bot")
	if err != nil {
		t.Fatal(err)
	}
	if res.MsgID != "MSG1" || res.ChatName != "Vini Eduqo" || !res.SentAt.Equal(t0Send) {
		t.Fatalf("result=%+v", res)
	}
	want := []string{"typing:true", "typing:false", "send:oi Vini, teste do bot"}
	if strings.Join(h.sender.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls=%v", h.sender.calls)
	}
	if len(h.slept) != 1 || h.slept[0] != TypingDelay(len("oi Vini, teste do bot")) {
		t.Fatalf("slept=%v", h.slept)
	}
	if n, _ := h.arch.CountSent(context.Background(), viniJID.String(), t0Send.Add(-time.Minute)); n != 1 {
		t.Fatalf("audit rows=%d", n)
	}
	// Not watched: the send is audited but no message row is archived.
	if c, _ := h.arch.GetChat(context.Background(), viniJID.String()); c.Messages != 0 {
		t.Fatalf("unwatched chat archived %d messages", c.Messages)
	}
}

func TestSendToWatchedChatIsArchivedAsViaShipyard(t *testing.T) {
	h := newOutbox(t)
	mustDo(t, h.arch.Watch(context.Background(), viniJID.String(), 0))
	if _, err := h.send("oi"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := h.arch.History(context.Background(), viniJID.String(), time.Time{}, 0)
	if len(msgs) != 1 || !msgs[0].FromMe || !msgs[0].ViaShipyard || msgs[0].SenderJID != ownPN.String() {
		t.Fatalf("archived=%+v", msgs)
	}
}

func TestSendRefusals(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*outboxHarness)
		req  SendRequest
		code SendCode
	}{
		{"empty text", nil, SendRequest{Chat: viniJID.String(), Text: "  \n"}, CodeInvalid},
		{"invalid utf8", nil, SendRequest{Chat: viniJID.String(), Text: "\xff\xfe"}, CodeInvalid},
		{"too long", func(h *outboxHarness) { h.cfg.MaxChars = 5 }, SendRequest{Chat: viniJID.String(), Text: "123456"}, CodeTooLong},
		{"bad jid", nil, SendRequest{Chat: "not-a-jid", Text: "oi"}, CodeInvalid},
		{"broadcast jid", nil, SendRequest{Chat: "status@broadcast", Text: "oi"}, CodeInvalid},
		{"unknown chat", nil, SendRequest{Chat: "5511000000000@s.whatsapp.net", Text: "oi"}, CodeUnknownChat},
		{"paused", func(h *outboxHarness) { h.cfg.Paused = true }, SendRequest{}, CodePaused},
		{"not allowed", func(h *outboxHarness) {
			mustDo(t, h.arch.SetSendAllowed(context.Background(), viniJID.String(), false))
		}, SendRequest{}, CodeNotAllowed},
		{"not connected", func(h *outboxHarness) { h.sender.connected = false }, SendRequest{}, CodeNotConnected},
		{"transport failure", func(h *outboxHarness) { h.sender.fail = errors.New("boom") }, SendRequest{}, CodeSendFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOutbox(t)
			if tc.mut != nil {
				tc.mut(h)
			}
			req := tc.req
			if req.Chat == "" {
				req = SendRequest{Chat: viniJID.String(), Text: "oi"}
			}
			_, err := h.ob.Send(context.Background(), req)
			if got := codeOf(err); got != tc.code {
				t.Fatalf("code=%q want %q (err %v)", got, tc.code, err)
			}
			if h.sender.sends() != 0 {
				t.Fatal("refused send reached the transport")
			}
			if n, _ := h.arch.CountSent(context.Background(), "", time.Time{}); n != 0 {
				t.Fatal("refused send was audited")
			}
		})
	}
}

func TestSendRequiresPriorConversation(t *testing.T) {
	h := newOutbox(t)
	ctx := context.Background()
	fresh := "5511977777777@s.whatsapp.net"
	mustDo(t, h.arch.UpsertChat(ctx, fresh, "Novo", archive.KindDM))
	mustDo(t, h.arch.SetSendAllowed(ctx, fresh, true))
	_, err := h.ob.Send(ctx, SendRequest{Chat: fresh, Text: "oi"})
	if codeOf(err) != CodeNoHistory {
		t.Fatalf("err=%v", err)
	}
	mustDo(t, h.arch.TouchActivity(ctx, fresh, t0Send))
	if _, err := h.ob.Send(ctx, SendRequest{Chat: fresh, Text: "oi"}); err != nil {
		t.Fatalf("after activity: %v", err)
	}
}

func TestRateLimitsPerChatAndGlobalSurviveRestart(t *testing.T) {
	h := newOutbox(t)
	h.cfg.Limits = SendLimits{PerChat: "2/10m", Global: "3/1h"}
	for i := 0; i < 2; i++ {
		if _, err := h.send(fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.send("m3"); codeOf(err) != CodeRateLimited {
		t.Fatalf("per-chat limit: %v", err)
	}

	// A new Outbox over the same archive (a daemon restart) still counts
	// the earlier sends: limits live in the audit log, not in memory.
	restarted := &Outbox{Archive: h.arch, Sender: h.sender, Config: h.ob.Config, Self: h.ob.Self,
		Log: h.ob.Log, Now: h.ob.Now, Sleep: h.ob.Sleep}
	if _, err := restarted.Send(context.Background(), SendRequest{Chat: viniJID.String(), Text: "m4"}); codeOf(err) != CodeRateLimited {
		t.Fatalf("after restart: %v", err)
	}

	// The window slides.
	h.now = h.now.Add(11 * time.Minute)
	if _, err := h.send("m5"); err != nil {
		t.Fatalf("after window: %v", err)
	}
	// Global: 3 sends in the hour now; a fourth chat-allowed send is refused.
	h.now = h.now.Add(11 * time.Minute)
	if _, err := h.send("m6"); codeOf(err) != CodeRateLimited || !strings.Contains(err.Error(), "across all chats") {
		t.Fatalf("global limit: %v", err)
	}
}

func TestIdempotencyKey(t *testing.T) {
	h := newOutbox(t)
	ctx := context.Background()
	req := SendRequest{Chat: viniJID.String(), Text: "uma vez só", IdempotencyKey: "run-42"}
	first, err := h.ob.Send(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.ob.Send(ctx, req)
	if err != nil || !again.Duplicate || again.MsgID != first.MsgID {
		t.Fatalf("replay=%+v err=%v", again, err)
	}
	if h.sender.sends() != 1 {
		t.Fatalf("sent %d times", h.sender.sends())
	}
	other := "5511977777777@s.whatsapp.net"
	mustDo(t, h.arch.UpsertChat(ctx, other, "Outro", archive.KindDM))
	if _, err := h.ob.Send(ctx, SendRequest{Chat: other, Text: "x", IdempotencyKey: "run-42"}); codeOf(err) != CodeInvalid {
		t.Fatalf("key reuse across chats: %v", err)
	}
}

func TestDryRunAndHumanizeOff(t *testing.T) {
	h := newOutbox(t)
	res, err := h.ob.Send(context.Background(), SendRequest{Chat: viniJID.String(), Text: "oi", DryRun: true})
	if err != nil || !res.DryRun || h.sender.sends() != 0 {
		t.Fatalf("dry run: %+v %v sends=%d", res, err, h.sender.sends())
	}
	h.cfg.Humanize = false
	if _, err := h.send("oi"); err != nil {
		t.Fatal(err)
	}
	if len(h.slept) != 0 || strings.Contains(strings.Join(h.sender.calls, ","), "typing") {
		t.Fatalf("humanize off still typed: slept=%v calls=%v", h.slept, h.sender.calls)
	}
}

func TestSendAbortedWhileTypingIsNotSent(t *testing.T) {
	h := newOutbox(t)
	h.ob.Sleep = func(ctx context.Context, _ time.Duration) error { return context.Canceled }
	if _, err := h.send("oi"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if h.sender.sends() != 0 {
		t.Fatal("cancelled send went out")
	}
}

func TestTypingDelayBounds(t *testing.T) {
	if d := TypingDelay(0); d != 1500*time.Millisecond {
		t.Fatalf("min=%v", d)
	}
	if d := TypingDelay(10_000); d != 8*time.Second {
		t.Fatalf("cap=%v", d)
	}
}

func TestConcurrentSendsRespectLimit(t *testing.T) {
	// Check-then-act under the Outbox lock: 10 racing sends against a
	// per-chat limit of 3 must yield exactly 3.
	h := newOutbox(t)
	h.cfg.Limits = SendLimits{PerChat: "3/10m", Global: "30/1h"}
	h.cfg.Humanize = false
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = h.ob.Send(context.Background(), SendRequest{Chat: viniJID.String(), Text: fmt.Sprintf("m%d", i)})
		}(i)
	}
	wg.Wait()
	if h.sender.sends() != 3 {
		t.Fatalf("sent %d, want 3", h.sender.sends())
	}
}
