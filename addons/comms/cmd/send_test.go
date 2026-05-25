package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/app"
	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms/provider"
)

// fakeProvider records the last Send call for assertions and returns a
// configurable Receipt/err.
type fakeProvider struct {
	receipt provider.Receipt
	err     error

	lastChannel comms.Channel
	lastSecret  []byte
	lastMsg     comms.Message
	calls       int
}

func (f *fakeProvider) Name() string { return "telegram" }
func (f *fakeProvider) Send(_ context.Context, ch comms.Channel, raw []byte, msg comms.Message) (provider.Receipt, error) {
	f.calls++
	f.lastChannel = ch
	f.lastSecret = raw
	f.lastMsg = msg
	return f.receipt, f.err
}

// withFakeProvider swaps the process-wide registry for one binding the
// telegram type to fake, restoring the prior registry on test cleanup.
func withFakeProvider(t *testing.T, fake *fakeProvider) {
	t.Helper()
	prev := app.Providers()
	r := provider.NewRegistry()
	r.Register(comms.ChannelTypeTelegram, fake)
	app.SetProviders(r)
	t.Cleanup(func() { app.SetProviders(prev) })
}

func seedChannel(t *testing.T) (comms.Channel, []byte) {
	t.Helper()
	// Test isolates SHIPYARD_HOME first via isolateHome (caller's job).
	if err := runChannelAdd(context.Background(), []string{
		"--type", "telegram",
		"--name", "tg-personal",
		"--telegram-token", "BOT-TOKEN-XYZ",
	}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	home, err := comms.CommsHome()
	if err != nil {
		t.Fatal(err)
	}
	chans, err := comms.NewChannelStore(home).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(chans) == 0 {
		t.Fatal("expected at least one channel after seed")
	}
	raw, err := comms.NewSecretStore(home).Read(chans[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return chans[0], raw
}

func TestSend_HappyPath_ByName(t *testing.T) {
	isolateHome(t)
	ch, secret := seedChannel(t)

	fake := &fakeProvider{
		receipt: provider.Receipt{ProviderMessageID: "42", SentAt: time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)},
	}
	withFakeProvider(t, fake)

	var stdout, stderr bytes.Buffer
	args := []string{"--channel", "tg-personal", "--to", "12345", "--text", "hello"}
	if err := runSend(context.Background(), args, nil, &stdout, &stderr); err != nil {
		t.Fatalf("send: %v\nstderr=%s", err, stderr.String())
	}

	if fake.calls != 1 {
		t.Errorf("provider.Send calls = %d, want 1", fake.calls)
	}
	if fake.lastChannel.ID != ch.ID {
		t.Errorf("channel ID mismatch: got %s want %s", fake.lastChannel.ID, ch.ID)
	}
	if !bytes.Equal(fake.lastSecret, secret) {
		t.Errorf("provider received wrong secret bytes")
	}
	if fake.lastMsg.Body != "hello" || fake.lastMsg.To[0].Raw != "12345" {
		t.Errorf("envelope unexpected: %+v", fake.lastMsg)
	}
	if !strings.Contains(stdout.String(), "Sent via tg-personal") {
		t.Errorf("missing confirmation: %q", stdout.String())
	}
}

func TestSend_HappyPath_ByID(t *testing.T) {
	isolateHome(t)
	ch, _ := seedChannel(t)
	withFakeProvider(t, &fakeProvider{receipt: provider.Receipt{ProviderMessageID: "1"}})

	var stdout, stderr bytes.Buffer
	args := []string{"--channel", ch.ID, "--to", "12345", "--text", "hi"}
	if err := runSend(context.Background(), args, nil, &stdout, &stderr); err != nil {
		t.Fatalf("send by id: %v", err)
	}
}

func TestSend_FromStdinBody(t *testing.T) {
	isolateHome(t)
	seedChannel(t)
	fake := &fakeProvider{receipt: provider.Receipt{ProviderMessageID: "9"}}
	withFakeProvider(t, fake)

	stdin := strings.NewReader("body from pipe")
	var stdout, stderr bytes.Buffer
	args := []string{"--channel", "tg-personal", "--to", "12345", "--stdin-body"}
	if err := runSend(context.Background(), args, stdin, &stdout, &stderr); err != nil {
		t.Fatalf("send --stdin-body: %v", err)
	}
	if fake.lastMsg.Body != "body from pipe" {
		t.Errorf("envelope body = %q, want 'body from pipe'", fake.lastMsg.Body)
	}
}

func TestSend_StdinAndTextMutuallyExclusive(t *testing.T) {
	isolateHome(t)
	seedChannel(t)
	withFakeProvider(t, &fakeProvider{})

	var stdout, stderr bytes.Buffer
	args := []string{"--channel", "tg-personal", "--to", "12345", "--text", "x", "--stdin-body"}
	err := runSend(context.Background(), args, strings.NewReader("y"), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutual-exclusion error, got %v", err)
	}
}

func TestSend_JSONOutput(t *testing.T) {
	isolateHome(t)
	seedChannel(t)
	withFakeProvider(t, &fakeProvider{
		receipt: provider.Receipt{ProviderMessageID: "abc", SentAt: time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)},
	})

	var stdout, stderr bytes.Buffer
	args := []string{"--channel", "tg-personal", "--to", "12345", "--text", "hi", "--json"}
	if err := runSend(context.Background(), args, nil, &stdout, &stderr); err != nil {
		t.Fatalf("send: %v", err)
	}
	var r struct {
		Channel           string `json:"channel"`
		ChannelName       string `json:"channel_name"`
		ProviderMessageID string `json:"provider_message_id"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		t.Fatalf("json output not parseable: %v\n%s", err, stdout.String())
	}
	if r.ChannelName != "tg-personal" || r.ProviderMessageID != "abc" {
		t.Errorf("unexpected json receipt: %+v", r)
	}
}

func TestSend_TraceIDPropagated(t *testing.T) {
	isolateHome(t)
	seedChannel(t)
	fake := &fakeProvider{receipt: provider.Receipt{ProviderMessageID: "1"}}
	withFakeProvider(t, fake)

	args := []string{"--channel", "tg-personal", "--to", "12345", "--text", "hi", "--trace-id", "trace-abc"}
	if err := runSend(context.Background(), args, nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if fake.lastMsg.TraceID != "trace-abc" {
		t.Errorf("envelope trace_id = %q, want trace-abc", fake.lastMsg.TraceID)
	}
}

func TestSend_CustomFromAddress(t *testing.T) {
	isolateHome(t)
	seedChannel(t)
	fake := &fakeProvider{receipt: provider.Receipt{ProviderMessageID: "1"}}
	withFakeProvider(t, fake)

	args := []string{"--channel", "tg-personal", "--to", "12345", "--text", "hi", "--from", "@mybot"}
	if err := runSend(context.Background(), args, nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if fake.lastMsg.From.Raw != "@mybot" {
		t.Errorf("envelope from.raw = %q, want @mybot", fake.lastMsg.From.Raw)
	}
}

func TestSend_RejectsMissingChannel(t *testing.T) {
	isolateHome(t)
	withFakeProvider(t, &fakeProvider{})
	err := runSend(context.Background(), []string{"--to", "12345", "--text", "hi"}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--channel is required") {
		t.Errorf("expected missing-channel error, got %v", err)
	}
}

func TestSend_RejectsMissingTo(t *testing.T) {
	isolateHome(t)
	withFakeProvider(t, &fakeProvider{})
	err := runSend(context.Background(), []string{"--channel", "x", "--text", "hi"}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--to is required") {
		t.Errorf("expected missing-to error, got %v", err)
	}
}

func TestSend_RejectsMissingBody(t *testing.T) {
	isolateHome(t)
	withFakeProvider(t, &fakeProvider{})
	err := runSend(context.Background(), []string{"--channel", "x", "--to", "12345"}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "body is required") {
		t.Errorf("expected missing-body error, got %v", err)
	}
}

func TestSend_UnknownChannel(t *testing.T) {
	isolateHome(t)
	withFakeProvider(t, &fakeProvider{})
	args := []string{"--channel", "nonexistent", "--to", "12345", "--text", "hi"}
	err := runSend(context.Background(), args, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no channel found") {
		t.Errorf("expected channel-not-found error, got %v", err)
	}
}

func TestSend_PropagatesProviderError(t *testing.T) {
	isolateHome(t)
	seedChannel(t)
	withFakeProvider(t, &fakeProvider{err: errors.New("telegram: chat not found")})

	args := []string{"--channel", "tg-personal", "--to", "12345", "--text", "hi"}
	err := runSend(context.Background(), args, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("expected provider error surfaced, got %v", err)
	}
}

func TestResolveChannel_NameWinsOverID(t *testing.T) {
	// Construct a corner case: a channel whose ID coincides with another
	// channel's Name. This is impossible under Validate (id is hex,
	// name starts with letter) but the precedence is documented so we
	// test the contract anyway.
	channels := []comms.Channel{
		{ID: "01htgkx7r4qx9z8m6n3v2bp4ay", Name: "matches-by-id"},
		{ID: "1111111111111111", Name: "01htgkx7r4qx9z8m6n3v2bp4ay"},
	}
	got, ok := resolveChannel(channels, "01htgkx7r4qx9z8m6n3v2bp4ay")
	if !ok {
		t.Fatal("expected to resolve")
	}
	if got.Name != "01htgkx7r4qx9z8m6n3v2bp4ay" {
		t.Errorf("expected Name match to win over ID, got %+v", got)
	}
}
