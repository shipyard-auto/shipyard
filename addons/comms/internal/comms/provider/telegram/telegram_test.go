package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
)

// fixedTS gives every test a deterministic non-zero envelope timestamp.
var fixedTS = time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

// fakeChannel returns a valid Telegram channel. The Provider doesn't
// actually use the channel fields in v1 — token comes from the secret —
// but Validate() upstream will be exercised in integration tests.
func fakeChannel() comms.Channel {
	return comms.Channel{
		ID:        "01htgkx7r4qx9z8m6n3v2bp4ay",
		Name:      "tg-bot",
		Type:      comms.ChannelTypeTelegram,
		CreatedAt: fixedTS,
		UpdatedAt: fixedTS,
	}
}

func validMessage() comms.Message {
	return comms.Message{
		SchemaVersion: comms.MessageSchemaVersion,
		Channel:       "01htgkx7r4qx9z8m6n3v2bp4ay",
		Direction:     comms.DirectionOutbound,
		From:          comms.Address{Channel: "01htgkx7r4qx9z8m6n3v2bp4ay", Raw: "@mybot"},
		To:            []comms.Address{{Channel: "01htgkx7r4qx9z8m6n3v2bp4ay", Raw: "12345"}},
		Body:          "hello",
		Timestamp:     fixedTS,
	}
}

func validSecret(t *testing.T, token string) []byte {
	t.Helper()
	b, err := json.Marshal(secret{Token: token})
	if err != nil {
		t.Fatalf("marshal secret: %v", err)
	}
	return b
}

// fakeAPI spins up an httptest server that records the requests it
// receives and lets each test stub the response per scenario.
type fakeAPI struct {
	t        *testing.T
	server   *httptest.Server
	requests []recordedRequest

	// status and body are returned for every request unless responseFn is set.
	status int
	body   string
	// responseFn is called for the recorded request and may write a
	// different body / status. Useful for token-path assertions.
	responseFn func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte)
}

type recordedRequest struct {
	Method      string
	Path        string
	ContentType string
	Form        url.Values
	BodyRaw     string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		t:      t,
		status: http.StatusOK,
		body:   `{"ok":true,"result":{"message_id":42}}`,
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ParseForm must run before we drain the body, otherwise it has
		// nothing to read for application/x-www-form-urlencoded.
		_ = r.ParseForm()
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		f.requests = append(f.requests, recordedRequest{
			Method:      r.Method,
			Path:        r.URL.Path,
			ContentType: r.Header.Get("Content-Type"),
			Form:        r.PostForm,
			BodyRaw:     string(b),
		})
		if f.responseFn != nil {
			f.responseFn(f.t, w, r, b)
			return
		}
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func newProvider(api *fakeAPI) *Provider {
	return &Provider{
		HTTPClient: api.server.Client(),
		APIBase:    api.server.URL,
		Now:        func() time.Time { return fixedTS },
	}
}

func TestProvider_Name(t *testing.T) {
	if got := New().Name(); got != "telegram" {
		t.Errorf("Name() = %q, want telegram", got)
	}
}

func TestSend_HappyPath(t *testing.T) {
	api := newFakeAPI(t)
	p := newProvider(api)

	receipt, err := p.Send(context.Background(), fakeChannel(), validSecret(t, "BOT-TOKEN-123"), validMessage())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if receipt.ProviderMessageID != "42" {
		t.Errorf("ProviderMessageID = %q, want 42", receipt.ProviderMessageID)
	}
	if !receipt.SentAt.Equal(fixedTS) {
		t.Errorf("SentAt = %v, want %v (Now stub)", receipt.SentAt, fixedTS)
	}

	if len(api.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(api.requests))
	}
	r := api.requests[0]
	if r.Method != http.MethodPost {
		t.Errorf("Method = %q, want POST", r.Method)
	}
	if r.Path != "/botBOT-TOKEN-123/sendMessage" {
		t.Errorf("Path = %q, want /botBOT-TOKEN-123/sendMessage", r.Path)
	}
	if r.ContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", r.ContentType)
	}
	if got := r.Form.Get("chat_id"); got != "12345" {
		t.Errorf("chat_id = %q, want 12345", got)
	}
	if got := r.Form.Get("text"); got != "hello" {
		t.Errorf("text = %q, want hello", got)
	}
}

func TestSend_APIError(t *testing.T) {
	api := newFakeAPI(t)
	api.status = http.StatusBadRequest
	api.body = `{"ok":false,"error_code":400,"description":"chat not found"}`
	p := newProvider(api)

	_, err := p.Send(context.Background(), fakeChannel(), validSecret(t, "BOT-TOKEN-123"), validMessage())
	if err == nil {
		t.Fatal("expected api error, got nil")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error should surface description, got %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should include error_code, got %v", err)
	}
}

func TestSend_UnparseableBody(t *testing.T) {
	api := newFakeAPI(t)
	api.body = `<html>upstream broke</html>`
	p := newProvider(api)

	_, err := p.Send(context.Background(), fakeChannel(), validSecret(t, "TOK"), validMessage())
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "<html>upstream broke</html>") {
		t.Errorf("error should include snippet of body, got %v", err)
	}
}

func TestSend_BodyTruncatedInError(t *testing.T) {
	api := newFakeAPI(t)
	api.body = strings.Repeat("X", 1024) // larger than 256-byte snippet cap
	p := newProvider(api)

	_, err := p.Send(context.Background(), fakeChannel(), validSecret(t, "TOK"), validMessage())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "…") {
		t.Errorf("expected truncation marker in error, got %v", err)
	}
}

func TestSend_EmptySecret(t *testing.T) {
	p := New()
	_, err := p.Send(context.Background(), fakeChannel(), nil, validMessage())
	if err == nil || !strings.Contains(err.Error(), "empty secret") {
		t.Errorf("expected 'empty secret' error, got %v", err)
	}
}

func TestSend_MalformedSecret(t *testing.T) {
	p := New()
	_, err := p.Send(context.Background(), fakeChannel(), []byte("{garbage"), validMessage())
	if err == nil || !strings.Contains(err.Error(), "parse secret") {
		t.Errorf("expected parse-secret error, got %v", err)
	}
}

func TestSend_MissingToken(t *testing.T) {
	p := New()
	_, err := p.Send(context.Background(), fakeChannel(), []byte(`{"token":""}`), validMessage())
	if err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Errorf("expected token-required error, got %v", err)
	}
}

func TestSend_RejectsMultipleRecipients(t *testing.T) {
	msg := validMessage()
	msg.To = append(msg.To, comms.Address{Channel: msg.Channel, Raw: "67890"})

	p := New()
	_, err := p.Send(context.Background(), fakeChannel(), validSecret(t, "TOK"), msg)
	if err == nil || !strings.Contains(err.Error(), "exactly 1 recipient") {
		t.Errorf("expected recipient-count error, got %v", err)
	}
}

func TestSend_RejectsEmptyRecipient(t *testing.T) {
	msg := validMessage()
	msg.To = []comms.Address{{Channel: msg.Channel}} // Raw empty

	p := New()
	_, err := p.Send(context.Background(), fakeChannel(), validSecret(t, "TOK"), msg)
	if err == nil || !strings.Contains(err.Error(), "to.raw") {
		t.Errorf("expected to.raw error, got %v", err)
	}
}

func TestSend_RejectsEmptyBody(t *testing.T) {
	msg := validMessage()
	msg.Body = ""

	p := New()
	_, err := p.Send(context.Background(), fakeChannel(), validSecret(t, "TOK"), msg)
	if err == nil || !strings.Contains(err.Error(), "body is required") {
		t.Errorf("expected body-required error, got %v", err)
	}
}

func TestSend_ContextCancelled(t *testing.T) {
	// httptest server that never responds — request will block until ctx
	// cancels, then HTTPClient.Do returns ctx.Err().
	api := newFakeAPI(t)
	api.responseFn = func(_ *testing.T, _ http.ResponseWriter, _ *http.Request, _ []byte) {
		time.Sleep(5 * time.Second)
	}
	p := newProvider(api)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Send(ctx, fakeChannel(), validSecret(t, "TOK"), validMessage())
	if err == nil {
		t.Fatal("expected context-cancellation error, got nil")
	}
}

func TestSend_TrailingSlashInAPIBase(t *testing.T) {
	api := newFakeAPI(t)
	p := newProvider(api)
	p.APIBase = api.server.URL + "/"

	if _, err := p.Send(context.Background(), fakeChannel(), validSecret(t, "TOK"), validMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if api.requests[0].Path != "/botTOK/sendMessage" {
		t.Errorf("trailing slash leaked into URL: %q", api.requests[0].Path)
	}
}

func TestSend_NilHTTPClient(t *testing.T) {
	p := &Provider{APIBase: "http://example", Now: func() time.Time { return fixedTS }}
	_, err := p.Send(context.Background(), fakeChannel(), validSecret(t, "TOK"), validMessage())
	if err == nil || !strings.Contains(err.Error(), "HTTPClient is nil") {
		t.Errorf("expected nil-HTTPClient error, got %v", err)
	}
}
