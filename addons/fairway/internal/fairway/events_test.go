package fairway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
	"github.com/shipyard-auto/shipyard/internal/signing"
)

// receiver is a subscriber endpoint that verifies every webhook exactly
// like an external system would: with its own client secret.
type receiver struct {
	t       *testing.T
	secret  func() string
	status  func(n int) int // status to answer on the n-th call (1-based)
	mu      sync.Mutex
	calls   int
	events  []fairway.Event
	attempt []string
	srv     *httptest.Server
}

func newReceiver(t *testing.T, secret func() string) *receiver {
	r := &receiver{t: t, secret: secret, status: func(int) int { return 200 }}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		p, err := signing.ParseHeaders(req.Header)
		if err == nil {
			err = signing.Check(r.secret(), p, req.Method, req.URL.Path, req.URL.RawQuery, body, time.Now())
		}
		r.mu.Lock()
		r.calls++
		n := r.calls
		r.mu.Unlock()
		if err != nil {
			t.Errorf("webhook signature does not verify: %v", err)
			w.WriteHeader(401)
			return
		}
		var ev fairway.Event
		_ = json.Unmarshal(body, &ev)
		if req.Header.Get(fairway.HeaderEventID) != ev.ID || req.Header.Get(fairway.HeaderEventTopic) != ev.Topic {
			t.Errorf("event headers %q/%q vs body %q/%q", req.Header.Get(fairway.HeaderEventID), req.Header.Get(fairway.HeaderEventTopic), ev.ID, ev.Topic)
		}
		st := r.status(n)
		if st >= 200 && st < 300 {
			r.mu.Lock()
			r.events = append(r.events, ev)
			r.attempt = append(r.attempt, req.Header.Get(fairway.HeaderDeliveryTry))
			r.mu.Unlock()
		}
		w.WriteHeader(st)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) got() []fairway.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]fairway.Event(nil), r.events...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type eventsEnv struct {
	clients *fairway.ClientStore
	subs    *fairway.SubscriptionStore
	disp    *fairway.Dispatcher
	ts      *httptest.Server
	secrets map[string]string
}

func newEventsEnv(t *testing.T, clients map[string][]string) *eventsEnv {
	t.Helper()
	dir := t.TempDir()
	cs, err := fairway.OpenClientStore(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := &eventsEnv{clients: cs, secrets: map[string]string{}}
	for id, scopes := range clients {
		c, err := cs.Add(id, scopes)
		if err != nil {
			t.Fatal(err)
		}
		e.secrets[id] = c.Secret
	}
	if e.subs, err = fairway.OpenSubscriptionStore(filepath.Join(dir, "subscriptions.json")); err != nil {
		t.Fatal(err)
	}
	e.disp = fairway.NewDispatcher(e.subs, cs, nil, "test")
	e.disp.Backoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.disp.Run(ctx)

	cfg := baseConfig()
	router := fairway.NewRouterWithConfig(&fakeRepo{cfg: cfg}, cfg)
	srv := fairway.NewServer(fairway.ServerConfig{Router: router, Executor: &fakeExecutor{}, Clients: cs, Events: e.disp})
	e.ts = httptest.NewServer(serverHandler(srv))
	t.Cleanup(e.ts.Close)
	return e
}

func (e *eventsEnv) secretOf(id string) func() string {
	return func() string { c, _ := e.clients.Lookup(id); return c.Secret }
}

func (e *eventsEnv) publish(t *testing.T, ev map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(ev)
	resp, err := http.Post(e.ts.URL+"/_shipyard/v1/events", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	out["_status"] = resp.StatusCode
	return out
}

func TestEventsFanOutHonoursTopicAndEventScope(t *testing.T) {
	e := newEventsEnv(t, map[string][]string{
		"promo":   {"events.subscribe:whatsapp.message", "whatsapp.read:120363@g.us"},
		"all":     {"events.*", "whatsapp.read"},
		"nothing": {"whatsapp.read"}, // may read, may not subscribe
	})
	rPromo, rAll := newReceiver(t, e.secretOf("promo")), newReceiver(t, e.secretOf("all"))
	if _, err := e.disp.Subscribe("promo", "whatsapp.message", rPromo.srv.URL+"/hook", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.disp.Subscribe("all", "whatsapp.*", rAll.srv.URL+"/hook?src=fw", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.disp.Subscribe("nothing", "whatsapp.message", "http://127.0.0.1:1/x", false); err == nil {
		t.Fatal("subscription without events.subscribe accepted")
	}

	// Event in the promo group: both get it. Event elsewhere: only "all".
	out := e.publish(t, map[string]any{"topic": "whatsapp.message", "scope": "whatsapp.read:120363@g.us", "source": "comms-whatsapp", "data": map[string]string{"body": "promo!"}})
	if out["_status"] != 202 || out["subscribers"] != float64(2) {
		t.Fatalf("publish=%v", out)
	}
	e.publish(t, map[string]any{"topic": "whatsapp.message", "scope": "whatsapp.read:999@g.us", "data": map[string]string{"body": "other chat"}})

	waitFor(t, func() bool { return len(rAll.got()) == 2 && len(rPromo.got()) == 1 })
	time.Sleep(50 * time.Millisecond) // nothing else should arrive
	if got := rPromo.got(); len(got) != 1 || !strings.Contains(string(got[0].Data), "promo!") || got[0].Source != "comms-whatsapp" || !strings.HasPrefix(got[0].ID, "evt_") {
		t.Fatalf("promo got %+v", got)
	}
}

func TestEventsRetryTransientFailures(t *testing.T) {
	e := newEventsEnv(t, map[string][]string{"promo": {"events.*"}})
	r := newReceiver(t, e.secretOf("promo"))
	r.status = func(n int) int {
		if n < 3 {
			return 503
		}
		return 200
	}
	if _, err := e.disp.Subscribe("promo", "jobs.done", r.srv.URL+"/hook", false); err != nil {
		t.Fatal(err)
	}
	e.publish(t, map[string]any{"topic": "jobs.done", "data": 1})
	waitFor(t, func() bool { return len(r.got()) == 1 })
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls != 3 || r.attempt[0] != "3" {
		t.Fatalf("calls=%d attempt=%v", r.calls, r.attempt)
	}
	views := e.disp.Views("")
	if views[0].Stats.Delivered != 1 || views[0].Stats.Failed != 2 {
		t.Fatalf("stats=%+v", views[0].Stats)
	}
}

func TestEventsPermanentFailureIsNotRetried(t *testing.T) {
	e := newEventsEnv(t, map[string][]string{"promo": {"events.*"}})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
	}))
	defer srv.Close()
	if _, err := e.disp.Subscribe("promo", "jobs.done", srv.URL, false); err != nil {
		t.Fatal(err)
	}
	e.publish(t, map[string]any{"topic": "jobs.done", "data": 1})
	waitFor(t, func() bool { return len(e.disp.Views("")) == 1 && e.disp.Views("")[0].Stats.Failed == 1 })
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 || e.disp.Views("")[0].Stats.LastStatus != 400 {
		t.Fatalf("calls=%d stats=%+v", calls.Load(), e.disp.Views("")[0].Stats)
	}
}

func TestEventsRevokedScopeStopsDelivery(t *testing.T) {
	e := newEventsEnv(t, map[string][]string{"promo": {"events.*"}})
	r := newReceiver(t, e.secretOf("promo"))
	if _, err := e.disp.Subscribe("promo", "jobs.done", r.srv.URL, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.clients.SetScopes("promo", []string{"whatsapp.read"}); err != nil {
		t.Fatal(err)
	}
	if out := e.publish(t, map[string]any{"topic": "jobs.done", "data": 1}); out["subscribers"] != float64(0) {
		t.Fatalf("revoked client still matched: %v", out)
	}
}

func TestPublishEndpointValidation(t *testing.T) {
	e := newEventsEnv(t, nil)
	for _, bad := range []map[string]any{
		{"topic": "Bad Topic", "data": 1},
		{"topic": "x.*", "data": 1},
		{"topic": "ok.topic", "scope": "Bad Scope", "data": 1},
	} {
		if out := e.publish(t, bad); out["_status"] != 400 {
			t.Errorf("%v accepted: %v", bad, out)
		}
	}
	if out := e.publish(t, map[string]any{"topic": "ok.topic", "id": "evt_custom", "data": 1}); out["_status"] != 202 || out["id"] != "evt_custom" {
		t.Fatalf("custom id: %v", out)
	}
}

func TestSubscriptionSelfServiceAPI(t *testing.T) {
	e := newEventsEnv(t, map[string][]string{
		"promo": {"events.subscribe:whatsapp.message"},
		"other": {"events.*"},
	})
	call := func(client, method, path string, body any) (int, []byte) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, e.ts.URL+path, bytes.NewReader(raw))
		if client != "" {
			for k, v := range signing.SignRequest(client, e.secrets[client], method, path, "", raw, time.Now()) {
				req.Header.Set(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}

	if st, _ := call("", http.MethodGet, "/_shipyard/v1/subscriptions", nil); st != 401 {
		t.Fatalf("unsigned list=%d", st)
	}
	st, out := call("promo", http.MethodPost, "/_shipyard/v1/subscriptions", map[string]string{"topic": "whatsapp.message", "url": "http://127.0.0.1:9100/hook"})
	if st != 201 {
		t.Fatalf("create=%d %s", st, out)
	}
	var sub fairway.Subscription
	_ = json.Unmarshal(out, &sub)
	if st, _ := call("promo", http.MethodPost, "/_shipyard/v1/subscriptions", map[string]string{"topic": "jobs.done", "url": "http://127.0.0.1:9100/hook"}); st != 403 {
		t.Fatalf("create without scope=%d", st)
	}
	if st, out := call("promo", http.MethodPost, "/_shipyard/v1/subscriptions", map[string]string{"topic": "whatsapp.message", "url": "http://promo.example.com/hook"}); st != 400 || !strings.Contains(string(out), "https") {
		t.Fatalf("plain http to a remote host=%d %s", st, out)
	}
	if _, err := e.disp.Subscribe("other", "jobs.done", "https://other.example.com/hook", false); err != nil {
		t.Fatal(err)
	}
	st, out = call("promo", http.MethodGet, "/_shipyard/v1/subscriptions", nil)
	if st != 200 || strings.Contains(string(out), "other.example.com") || !strings.Contains(string(out), sub.ID) {
		t.Fatalf("list own=%d %s", st, out)
	}
	others := e.subs.List("other")
	if st, _ := call("promo", http.MethodDelete, "/_shipyard/v1/subscriptions/"+others[0].ID, nil); st != 404 {
		t.Fatalf("deleting another client's subscription=%d", st)
	}
	if st, _ := call("promo", http.MethodDelete, "/_shipyard/v1/subscriptions/"+sub.ID, nil); st != 204 {
		t.Fatalf("delete own=%d", st)
	}
}

func TestWebhookURLAndReservedPaths(t *testing.T) {
	cases := []struct {
		url   string
		allow bool
		ok    bool
	}{
		{"https://promo.example.com/hook", false, true},
		{"http://127.0.0.1:9100/hook", false, true},
		{"http://localhost/hook", false, true},
		{"http://promo.example.com/hook", false, false},
		{"http://10.0.0.5/hook", true, true},
		{"ftp://x/y", false, false},
		{"/relative", false, false},
	}
	for _, tc := range cases {
		if err := fairway.ValidateWebhookURL(tc.url, tc.allow); (err == nil) != tc.ok {
			t.Errorf("%s allow=%v: err=%v", tc.url, tc.allow, err)
		}
	}
	for _, p := range []string{"/_shipyard/v1/events", "/_shipyard", "/_health"} {
		r := fairway.Route{Path: p, Auth: fairway.Auth{Type: fairway.AuthLocalOnly}, Action: fairway.Action{Type: fairway.ActionCronRun, Target: "x"}}
		if err := r.Validate(); err == nil {
			t.Errorf("reserved path %s accepted", p)
		}
	}
}

func TestPingReachesOneSubscription(t *testing.T) {
	e := newEventsEnv(t, map[string][]string{"promo": {"events.subscribe:whatsapp.message"}})
	r := newReceiver(t, e.secretOf("promo"))
	sub, err := e.disp.Subscribe("promo", "whatsapp.message", r.srv.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.disp.Ping(sub.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(r.got()) == 1 })
	if r.got()[0].Topic != fairway.PingTopic {
		t.Fatalf("got %+v", r.got())
	}
	if _, err := e.disp.Ping("sub_missing"); err == nil {
		t.Fatal("ping to a missing subscription succeeded")
	}
}

func TestWebhookSignsDecodedPath(t *testing.T) {
	e := newEventsEnv(t, map[string][]string{"promo": {"events.*"}})
	r := newReceiver(t, e.secretOf("promo"))
	if _, err := e.disp.Subscribe("promo", "jobs.done", r.srv.URL+"/hooks/minha%20loja?x=a%2Bb", false); err != nil {
		t.Fatal(err)
	}
	e.publish(t, map[string]any{"topic": "jobs.done", "data": 1})
	waitFor(t, func() bool { return len(r.got()) == 1 }) // receiver verifies with r.URL.Path
}

func TestSocketSubscriptionMethodsAndClientCascade(t *testing.T) {
	dir := t.TempDir()
	clients, _ := fairway.OpenClientStore(filepath.Join(dir, "clients.json"))
	if _, err := clients.Add("promo", []string{"events.*"}); err != nil {
		t.Fatal(err)
	}
	subs, _ := fairway.OpenSubscriptionStore(filepath.Join(dir, "subscriptions.json"))
	disp := fairway.NewDispatcher(subs, clients, nil, "test")
	cfg := defaultSocketCfg("v1.2.3", newTestRouter(t), nil)
	cfg.Clients, cfg.Events = clients, disp
	conn, sc := dialAndHandshake(t, startSocket(t, cfg), "v1.2.3")

	r := call(t, conn, sc, 2, "subscription.add", map[string]any{"clientId": "promo", "topic": "jobs.*", "url": "https://promo.example.com/hook"})
	if r.Error != nil {
		t.Fatalf("add: %+v", r.Error)
	}
	if r := call(t, conn, sc, 3, "subscription.add", map[string]any{"clientId": "promo", "topic": "jobs.*", "url": "http://promo.example.com/hook"}); r.Error == nil {
		t.Fatal("insecure URL accepted without allowHttp")
	}
	if r := call(t, conn, sc, 4, "subscription.add", map[string]any{"clientId": "promo", "topic": "jobs.*", "url": "http://10.0.0.5/hook", "allowHttp": true}); r.Error != nil {
		t.Fatalf("admin allowHttp: %+v", r.Error)
	}
	list := call(t, conn, sc, 5, "subscription.list", nil)
	if items, _ := list.Result.([]any); len(items) != 2 {
		t.Fatalf("list=%v", list.Result)
	}
	id := subs.List("")[0].ID
	if r := call(t, conn, sc, 6, "subscription.test", map[string]any{"id": id}); r.Error != nil {
		t.Fatalf("test: %+v", r.Error)
	}
	// Deleting the client removes its subscriptions too.
	if r := call(t, conn, sc, 7, "client.delete", map[string]any{"id": "promo"}); r.Error != nil {
		t.Fatalf("client.delete: %+v", r.Error)
	}
	if n := len(subs.List("")); n != 0 {
		t.Fatalf("%d subscriptions survived their client", n)
	}
}
