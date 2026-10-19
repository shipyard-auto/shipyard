package wa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// newSendFixture serves the API over an Outbox whose happy path covers
// Vini (DM) and the "Promos Pet" group: catalogued, allowlisted, active,
// with groups enabled.
func newSendFixture(t *testing.T) (*apiFixture, *outboxHarness) {
	t.Helper()
	h := newOutbox(t)
	ctx := context.Background()
	mustDo(t, h.arch.UpsertChat(ctx, promoA, "Promos Pet", archive.KindGroup))
	mustDo(t, h.arch.SetSendAllowed(ctx, promoA, true))
	mustDo(t, h.arch.TouchActivity(ctx, promoA, t0Send.Add(-time.Hour)))
	mustDo(t, h.arch.UpsertChat(ctx, promoB, "Promos Viagem", archive.KindGroup))
	h.cfg.AllowGroups = true
	api := &APIServer{
		Archive: h.arch,
		Token:   func() (string, error) { return testAPIToken, nil },
		Self:    func() types.JID { return ownPN },
		Log:     slog.New(yardlogs.NopHandler()),
		Outbox:  h.ob,
	}
	up, err := NewUploadStore(filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	api.Uploads = up
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &apiFixture{arch: h.arch, srv: srv}, h
}

func TestAPISendThroughPolicyAndAudit(t *testing.T) {
	f, h := newSendFixture(t)
	scopes := "whatsapp.read:*,whatsapp.send:" + promoA
	var res SendResult
	code := f.call(t, APISendPath, "promo-manager", scopes,
		SendAPIRequest{Chat: promoA, Text: "Ração 30% off hoje", IdempotencyKey: "promo-1"}, &res)
	if code != 200 || res.MsgID != "MSG1" || res.Chat != promoA || res.ChatName != "Promos Pet" || res.Duplicate {
		t.Fatalf("send → %d %+v", code, res)
	}
	// Humanize ran, as for any other sender.
	if len(h.slept) != 1 {
		t.Fatalf("humanize skipped: %v", h.slept)
	}
	// Retry with the same key: replayed, not resent.
	res = SendResult{}
	f.call(t, APISendPath, "promo-manager", scopes, SendAPIRequest{Chat: promoA, Text: "Ração 30% off hoje", IdempotencyKey: "promo-1"}, &res)
	if !res.Duplicate || res.MsgID != "MSG1" || h.sender.sends() != 1 {
		t.Fatalf("retry → %+v sends=%d", res, h.sender.sends())
	}
	// Keys are per client: another client with the same key really sends.
	res = SendResult{}
	f.call(t, APISendPath, "other", "whatsapp.send", SendAPIRequest{Chat: promoA, Text: "outra", IdempotencyKey: "promo-1"}, &res)
	if res.Duplicate || h.sender.sends() != 2 {
		t.Fatalf("cross-client key → %+v sends=%d", res, h.sender.sends())
	}
	// The audit log names the requester; local sends stay anonymous.
	if _, err := h.ob.Send(context.Background(), SendRequest{Chat: viniJID.String(), Text: "do cli"}); err != nil {
		t.Fatal(err)
	}
	log, err := h.arch.ListSent(context.Background(), t0Send.Add(-time.Hour), "", 0)
	if err != nil || len(log) != 3 {
		t.Fatalf("audit=%+v err=%v", log, err)
	}
	byClient := map[string]string{}
	for _, e := range log {
		byClient[e.MsgID] = e.Client + "|" + e.IdempotencyKey
	}
	if byClient["MSG1"] != "promo-manager|promo-manager/promo-1" || byClient["MSG2"] != "other|other/promo-1" || byClient["MSG3"] != "|" {
		t.Fatalf("audit clients=%v", byClient)
	}
	if mine, _ := h.arch.ListSent(context.Background(), t0Send.Add(-time.Hour), "promo-manager", 0); len(mine) != 1 {
		t.Fatalf("filter by client=%+v", mine)
	}
	if local, _ := h.arch.ListSent(context.Background(), t0Send.Add(-time.Hour), "local", 0); len(local) != 1 || local[0].MsgID != "MSG3" {
		t.Fatalf("filter local=%+v", local)
	}
}

func TestAPISendScopeIsNeededButNeverEnough(t *testing.T) {
	f, h := newSendFixture(t)
	var e apiErrorBody
	// Read scope does not allow sending; unknown and foreign chats look alike.
	for _, chat := range []string{promoA, promoB, "Ninguém"} {
		e = apiErrorBody{}
		if code := f.call(t, APISendPath, "promo", "whatsapp.read", SendAPIRequest{Chat: chat, Text: "x"}, &e); code != 403 || e.Error.Code != "forbidden" {
			t.Fatalf("read-only %q → %d %+v", chat, code, e)
		}
	}
	// Scope granted, but the owner's policy still decides.
	for name, tc := range map[string]struct {
		setup func()
		chat  string
		code  int
		err   string
	}{
		"not allowlisted": {func() {}, promoB, 403, "not_allowed"},
		"groups off":      {func() { h.cfg.AllowGroups = false }, promoA, 403, "groups_disabled"},
		"paused":          {func() { h.cfg.Paused = true }, promoA, 503, "paused"},
		"disconnected":    {func() { h.sender.connected = false }, promoA, 503, "not_connected"},
		"too long":        {func() { h.cfg.MaxChars = 3 }, promoA, 400, "too_long"},
	} {
		h.cfg, h.sender.connected = DefaultConfig(), true
		h.cfg.AllowGroups = true
		tc.setup()
		e = apiErrorBody{}
		if code := f.call(t, APISendPath, "promo", "whatsapp.send", SendAPIRequest{Chat: tc.chat, Text: "oferta"}, &e); code != tc.code || e.Error.Code != tc.err {
			t.Errorf("%s → %d %+v", name, code, e)
		}
	}
	if h.sender.sends() != 0 {
		t.Fatalf("refused sends went out: %d", h.sender.sends())
	}
	e = apiErrorBody{}
	if code := f.call(t, APISendPath, "promo", "whatsapp.send", SendAPIRequest{Text: "sem chat"}, &e); code != 400 {
		t.Fatalf("missing chat → %d", code)
	}
}

func TestAPISendRateLimitedCarriesRetryAfter(t *testing.T) {
	f, h := newSendFixture(t)
	h.cfg.Limits.PerGroup = "1/1h"
	var res SendResult
	if code := f.call(t, APISendPath, "promo", "whatsapp.send", SendAPIRequest{Chat: promoA, Text: "1"}, &res); code != 200 {
		t.Fatalf("first → %d", code)
	}
	h.now = t0Send.Add(10 * time.Minute)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+APISendPath, strings.NewReader(`{"chat":"`+promoA+`","text":"2"}`))
	req.Header.Set("Authorization", "Bearer "+testAPIToken)
	req.Header.Set("X-Shipyard-Client-Id", "promo")
	req.Header.Set("X-Shipyard-Client-Scopes", "whatsapp.send")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "3000" {
		t.Fatalf("status=%d retry-after=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}

	// /limits explains it to the client before it tries.
	var rep LimitsReport
	if code := f.call(t, APILimitsPath, "promo", "whatsapp.send:"+promoA, LimitsAPIRequest{Chat: promoA}, &rep); code != 200 {
		t.Fatalf("limits → %d", code)
	}
	if rep.Chat == nil || rep.Chat.CanSendNow || rep.Chat.RetryAfterSeconds != 3000 || rep.Chat.Usage.Scope != "per-group" ||
		rep.Global.Used != 1 || !rep.AllowGroups {
		t.Fatalf("limits=%+v chat=%+v", rep, rep.Chat)
	}
	var e apiErrorBody
	if code := f.call(t, APILimitsPath, "promo", "whatsapp.send:"+promoA, LimitsAPIRequest{Chat: promoB}, &e); code != 403 {
		t.Fatalf("limits on foreign chat → %d", code)
	}
	rep = LimitsReport{}
	if code := f.call(t, APILimitsPath, "promo", "whatsapp.send:"+promoA, LimitsAPIRequest{}, &rep); code != 200 || rep.Chat != nil || rep.PerGroup.Limit != "1/1h" {
		t.Fatalf("global limits → %d %+v", code, rep)
	}
}

func TestAPISendDryRunAndNoOutbox(t *testing.T) {
	f, h := newSendFixture(t)
	var res SendResult
	if code := f.call(t, APISendPath, "promo", "whatsapp.send", SendAPIRequest{Chat: "Promos Pet", Text: "teste", DryRun: true}, &res); code != 200 || !res.DryRun {
		t.Fatalf("dry run → %d %+v", code, res)
	}
	if h.sender.sends() != 0 {
		t.Fatal("dry run sent")
	}
	plain := newAPIFixture(t) // no Outbox
	var e apiErrorBody
	if code := plain.call(t, APISendPath, "promo", "whatsapp.send", SendAPIRequest{Chat: promoA, Text: "x"}, &e); code != 503 {
		t.Fatalf("no outbox → %d", code)
	}
}

func TestSocketCannotClaimAClient(t *testing.T) {
	var req SendRequest
	if err := json.Unmarshal([]byte(`{"chat":"x","text":"y","client":"promo-manager","Client":"promo-manager"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Client != "" {
		t.Fatalf("client taken from the wire: %q", req.Client)
	}
}

func TestAPISendMediaInlineAndUploaded(t *testing.T) {
	f, h := newSendFixture(t)
	jpg := testJPEG(t, 32, 32)
	b64 := base64.StdEncoding.EncodeToString

	// Inline: one call.
	var res SendResult
	code := f.call(t, APISendPath, "promo", "whatsapp.send:"+promoA,
		SendAPIRequest{Chat: promoA, Text: "Oferta do dia", IdempotencyKey: "img-1", Media: &SendAPIMedia{Data: b64(jpg)}}, &res)
	if code != 200 || res.Kind != SendKindImage || len(h.sender.media) != 1 || h.sender.media[0].Caption != "Oferta do dia" {
		t.Fatalf("inline → %d %+v", code, res)
	}

	// Chunked upload, then one send per group referencing it.
	pdf := append(append([]byte{}, testPDF...), bytes.Repeat([]byte("x"), 300)...)
	var up UploadInfo
	if code := f.call(t, APIUploadPath, "promo", "whatsapp.send:"+promoA, UploadAPIRequest{Data: b64(pdf[:100])}, &up); code != 200 || up.Size != 100 {
		t.Fatalf("chunk 1 → %d %+v", code, up)
	}
	var e apiErrorBody
	if code := f.call(t, APIUploadPath, "promo", "whatsapp.send:"+promoA,
		UploadAPIRequest{UploadID: up.UploadID, Offset: 50, Data: b64(pdf[100:])}, &e); code != 409 || e.Error.Code != "offset_mismatch" {
		t.Fatalf("bad offset → %d %+v", code, e)
	}
	if code := f.call(t, APIUploadPath, "promo", "whatsapp.send:"+promoA,
		UploadAPIRequest{UploadID: up.UploadID, Offset: 100, Data: b64(pdf[100:])}, &up); code != 200 || up.Size != int64(len(pdf)) {
		t.Fatalf("chunk 2 → %d %+v", code, up)
	}
	sum := sha256.Sum256(pdf)
	if up.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("upload sha256")
	}
	res = SendResult{}
	if code := f.call(t, APISendPath, "promo", "whatsapp.send:"+promoA,
		SendAPIRequest{Chat: promoA, Text: "Tabela", Media: &SendAPIMedia{UploadID: up.UploadID, FileName: "tabela.pdf"}}, &res); code != 200 || res.Kind != SendKindFile {
		t.Fatalf("send upload → %d %+v", code, res)
	}
	if got := h.sender.media[1]; !bytes.Equal(got.Data, pdf) || got.FileName != "tabela.pdf" || got.MimeType != "application/pdf" {
		t.Fatalf("sent document=%s %q", got.MimeType, got.FileName)
	}

	// Another client cannot use it; it looks like it never existed.
	e = apiErrorBody{}
	if code := f.call(t, APISendPath, "other", "whatsapp.send",
		SendAPIRequest{Chat: promoA, Media: &SendAPIMedia{UploadID: up.UploadID}}, &e); code != 404 || e.Error.Code != "unknown_upload" {
		t.Fatalf("foreign upload → %d %+v", code, e)
	}
	// Inline is capped at one chunk; bigger files must be uploaded.
	e = apiErrorBody{}
	big := make([]byte, APIChunkMax+1)
	if code := f.call(t, APISendPath, "promo", "whatsapp.send",
		SendAPIRequest{Chat: promoA, Media: &SendAPIMedia{Data: b64(big)}}, &e); code != 413 || !strings.Contains(e.Error.Message, APIUploadPath) {
		t.Fatalf("inline too big → %d %+v", code, e)
	}
	e = apiErrorBody{}
	if code := f.call(t, APISendPath, "promo", "whatsapp.send",
		SendAPIRequest{Chat: promoA, Media: &SendAPIMedia{Data: b64(jpg), UploadID: up.UploadID}}, &e); code != 400 {
		t.Fatalf("data and upload_id → %d", code)
	}
	// Uploading needs a send scope.
	e = apiErrorBody{}
	if code := f.call(t, APIUploadPath, "reader", "whatsapp.read", UploadAPIRequest{Data: b64(jpg)}, &e); code != 403 {
		t.Fatalf("upload without send scope → %d", code)
	}
	// Policy refusals keep their codes for media too.
	h.cfg.AllowGroups = false
	e = apiErrorBody{}
	if code := f.call(t, APISendPath, "promo", "whatsapp.send",
		SendAPIRequest{Chat: promoA, Media: &SendAPIMedia{Data: b64(jpg)}}, &e); code != 403 || e.Error.Code != "groups_disabled" {
		t.Fatalf("groups off → %d %+v", code, e)
	}
}
