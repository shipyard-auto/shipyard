package wa

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

func testDeliverer(t *testing.T, srv *httptest.Server) *Deliverer {
	t.Helper()
	d := NewDeliverer(func() string { return strings.TrimPrefix(srv.URL, "http://") }, slog.New(yardlogs.NopHandler()))
	d.Backoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	return d
}

func runUntil(t *testing.T, d *Deliverer, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for deliveries")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func sampleEvent() Event {
	return Event{SchemaVersion: 1, ID: "m1", TraceID: "trace-1", Channel: EventChannel, Direction: "inbound",
		Body: "oi", Metadata: map[string]string{"chat_jid": viniJID.String()}, Timestamp: t0Send}
}

func TestDeliverPostsEnvelopeWithTrace(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotTrace string
	var got Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath, gotTrace = r.URL.Path, r.Header.Get("X-Trace-Id")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	d := testDeliverer(t, srv)
	if !d.Enqueue("/wa/vini", sampleEvent()) {
		t.Fatal("enqueue refused")
	}
	runUntil(t, d, func() bool { n, _ := d.Counts(); return n == 1 })
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/wa/vini" || gotTrace != "trace-1" || got.Body != "oi" || got.SchemaVersion != 1 {
		t.Fatalf("path=%q trace=%q event=%+v", gotPath, gotTrace, got)
	}
}

func TestDeliverRetriesTransientButNotPermanent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch {
		case r.URL.Path == "/missing":
			w.WriteHeader(http.StatusNotFound)
		case n < 3:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	d := testDeliverer(t, srv)
	d.Enqueue("/flaky", sampleEvent())
	runUntil(t, d, func() bool { n, _ := d.Counts(); return n == 1 })
	if calls.Load() != 3 {
		t.Fatalf("transient: %d calls, want 3 (2 failures + success)", calls.Load())
	}

	calls.Store(10) // past the flaky phase
	d2 := testDeliverer(t, srv)
	d2.Enqueue("/missing", sampleEvent())
	runUntil(t, d2, func() bool { _, f := d2.Counts(); return f == 1 })
	if calls.Load() != 11 {
		t.Fatalf("404 retried: %d calls", calls.Load()-10)
	}
}

func TestDeliverGivesUpAfterBackoff(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	d := testDeliverer(t, srv)
	d.Enqueue("/down", sampleEvent())
	runUntil(t, d, func() bool { _, f := d.Counts(); return f == 1 })
	if calls.Load() != 4 {
		t.Fatalf("calls=%d want 4 (1 + 3 retries)", calls.Load())
	}
}

func TestEnqueueDropsWhenFull(t *testing.T) {
	d := NewDeliverer(func() string { return "127.0.0.1:1" }, slog.New(yardlogs.NopHandler()))
	for i := 0; i < DeliveryQueueSize; i++ {
		if !d.Enqueue("/x", sampleEvent()) {
			t.Fatalf("enqueue %d refused before the queue was full", i)
		}
	}
	if d.Enqueue("/x", sampleEvent()) {
		t.Fatal("enqueue past capacity accepted")
	}
	if _, f := d.Counts(); f != 1 {
		t.Fatalf("dropped count=%d", f)
	}
}

func TestFairwayAddr(t *testing.T) {
	dir := t.TempDir()
	if got := FairwayAddr("", dir); got != "127.0.0.1:9876" {
		t.Fatalf("no file: %s", got)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "routes.json"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"port": 9999, "bind": "127.0.0.1"}`)
	if got := FairwayAddr("", dir); got != "127.0.0.1:9999" {
		t.Fatalf("from file: %s", got)
	}
	write(`{"port": 8080, "bind": "0.0.0.0"}`)
	if got := FairwayAddr("", dir); got != "127.0.0.1:8080" {
		t.Fatalf("wildcard bind: %s", got)
	}
	if got := FairwayAddr("10.0.0.5:7000", dir); got != "10.0.0.5:7000" {
		t.Fatalf("override: %s", got)
	}
}

// Handler side: which messages become events, and their shape.
func TestHandlerRoutesNewMessages(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	var mu sync.Mutex
	var sent []struct {
		path string
		ev   Event
	}
	routes := Routes{ByChat: map[string]string{momPN.String(): "/wa/mae"}}
	hs.h.Routes = func() Routes { return routes }
	hs.h.Deliver = func(path string, ev Event) bool {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, struct {
			path string
			ev   Event
		}{path, ev})
		return true
	}
	mustDo(t, hs.arch.UpsertChat(ctx, momPN.String(), "Mãe", archive.KindDM))

	// Incoming DM by LID from a routed contact → one event, filed under the phone JID.
	hs.h.Handle(ctx, message(types.MessageSource{Chat: momLID, Sender: momLID, SenderAlt: momPN}, "m1", text("vem almoçar?")))
	// Own message: skipped by default.
	hs.h.Handle(ctx, message(types.MessageSource{Chat: momPN, Sender: ownPN, IsFromMe: true}, "m2", text("vou")))
	// Unrouted chat: skipped.
	hs.h.Handle(ctx, message(types.MessageSource{Chat: family, Sender: momPN, IsGroup: true}, "m3", text("oi")))
	// An edit of a routed message: not delivered.
	hs.h.Handle(ctx, &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: momPN, Sender: momPN}, ID: "e1", Timestamp: ts},
		Message: editOf("m1", "vem almoçar amanhã?")})

	if len(sent) != 1 {
		t.Fatalf("events=%+v", sent)
	}
	ev := sent[0].ev
	if sent[0].path != "/wa/mae" || ev.ID != "m1" || ev.Body != "vem almoçar?" || ev.Channel != "whatsapp" ||
		ev.From.Raw != momPN.String() || ev.From.Display != "Mãe" || ev.To[0].Raw != ownPN.String() ||
		ev.Metadata["chat_jid"] != momPN.String() || ev.Metadata["chat_name"] != "Mãe" || ev.Metadata["chat_kind"] != "dm" ||
		ev.Metadata["from_me"] != "false" || ev.Metadata["kind"] != "text" || ev.TraceID == "" || !ev.Timestamp.Equal(ts) {
		t.Fatalf("event=%+v", ev)
	}

	// include_own + default route.
	routes = Routes{Default: "/wa/inbox", IncludeOwn: true}
	hs.h.Handle(ctx, message(types.MessageSource{Chat: family, Sender: ownPN, IsFromMe: true, IsGroup: true}, "m4", text("bom dia")))
	if len(sent) != 2 || sent[1].path != "/wa/inbox" || sent[1].ev.Metadata["from_me"] != "true" || sent[1].ev.Metadata["chat_kind"] != "group" {
		t.Fatalf("default/include_own=%+v", sent)
	}
}

func TestConfigCacheReloadsOnChangeAndKeepsLastGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cache := NewConfigCache(path)
	if c, err := cache.Get(); err != nil || c.Paused {
		t.Fatalf("missing file: %+v %v", c, err)
	}
	cfg := DefaultConfig()
	cfg.Paused = true
	mustDo(t, SaveConfig(path, cfg))
	if c, _ := cache.Get(); !c.Paused {
		t.Fatal("cache did not pick up the new file")
	}
	// A broken edit keeps serving the last good config, with the error.
	mustDo(t, os.WriteFile(path, []byte("{broken"), 0o600))
	future := time.Now().Add(time.Second)
	mustDo(t, os.Chtimes(path, future, future))
	c, err := cache.Get()
	if err == nil || !c.Paused {
		t.Fatalf("broken file: %+v %v", c, err)
	}
}

func TestPublishPostsScopedEvent(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotTrace string
	var got PublishedEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath, gotTrace = r.URL.Path, r.Header.Get("X-Trace-Id")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	d := testDeliverer(t, srv)
	if !d.Publish(sampleEvent()) {
		t.Fatal("publish refused")
	}
	runUntil(t, d, func() bool { n, _ := d.Counts(); return n == 1 })
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/_shipyard/v1/events" || gotTrace != "trace-1" {
		t.Fatalf("path=%q trace=%q", gotPath, gotTrace)
	}
	if got.ID != "evt_wa_m1" || got.Topic != "whatsapp.message" || got.Scope != "whatsapp.read:"+viniJID.String() ||
		got.Source != "comms-whatsapp" || !got.OccurredAt.Equal(t0Send) || got.Data.Body != "oi" || got.Data.SchemaVersion != 1 {
		t.Fatalf("event=%+v", got)
	}
}

func TestHandlerPublishesSelectedChats(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	var mu sync.Mutex
	var published []string
	var delivered []string
	var sel PublishSelection
	routes := Routes{ByChat: map[string]string{momPN.String(): "/wa/mae"}}
	hs.h.Routes = func() Routes { return routes }
	hs.h.Deliver = func(path string, _ Event) bool {
		mu.Lock()
		defer mu.Unlock()
		delivered = append(delivered, path)
		return true
	}
	hs.h.Publishes = func() PublishSelection { return sel }
	hs.h.Publish = func(ev Event) bool {
		mu.Lock()
		defer mu.Unlock()
		published = append(published, ev.ID+"@"+ev.Metadata["chat_jid"])
		return true
	}
	hs.watch(t, family, "Família", archive.KindGroup)
	n := 0
	send := func(chat types.JID, group bool) {
		n++
		hs.h.Handle(ctx, message(types.MessageSource{Chat: chat, Sender: momPN, IsGroup: group}, fmt.Sprintf("m%d", n), text("oi")))
	}
	check := func(label string, want ...string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if strings.Join(published, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: published=%v want %v", label, published, want)
		}
		published = nil
	}

	// Off: nothing is published; routes still fire.
	send(momPN, false)
	check("off")
	if len(delivered) != 1 {
		t.Fatalf("route did not fire: %v", delivered)
	}
	// watched: the watched group only, not the unwatched DM.
	sel = PublishSelection{PublishWatched}
	send(momPN, false)
	send(family, true)
	check("watched", "m3@"+family.String())
	// Unwatching stops it without touching the selection.
	if _, err := hs.arch.Unwatch(ctx, family.String()); err != nil {
		t.Fatal(err)
	}
	send(family, true)
	check("after unwatch")
	// An explicit chat, watched or not.
	sel = PublishSelection{momPN.String()}
	send(momPN, false)
	send(family, true)
	check("explicit", "m5@"+momPN.String())
	// Everything.
	sel = PublishSelection{PublishAll}
	send(momPN, false)
	send(family, true)
	check("all", "m7@"+momPN.String(), "m8@"+family.String())
	// Own messages follow include_own, whatever the selection.
	hs.h.Handle(ctx, message(types.MessageSource{Chat: family, Sender: ownPN, IsFromMe: true, IsGroup: true}, "own1", text("meu")))
	check("own skipped")
	routes.IncludeOwn = true
	hs.h.Handle(ctx, message(types.MessageSource{Chat: family, Sender: ownPN, IsFromMe: true, IsGroup: true}, "own2", text("meu")))
	check("include_own", "own2@"+family.String())
}

func TestPublishMembersPostsMembershipScopedEvent(t *testing.T) {
	var mu sync.Mutex
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	d := testDeliverer(t, srv)
	via := ViaInviteLink
	if !d.PublishMembers(MemberEvent{SchemaVersion: 1, ID: "evt_wa_gm_x", Timestamp: t0Send, ChatJID: promoA, ChatName: "Fisgou",
		Action: "join", Member: "abc", Via: &via}) {
		t.Fatal("publish refused")
	}
	runUntil(t, d, func() bool { n, _ := d.Counts(); return n == 1 })
	mu.Lock()
	defer mu.Unlock()
	data, _ := got["data"].(map[string]any)
	if got["id"] != "evt_wa_gm_x" || got["topic"] != "whatsapp.group.members" || got["scope"] != "whatsapp.members:"+promoA ||
		data["member"] != "abc" || data["via"] != "invite_link" || data["actor"] != nil {
		t.Fatalf("event=%v", got)
	}
	if _, has := data["actor"]; !has {
		t.Fatal("actor must be present as null")
	}
}
