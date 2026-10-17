package fairway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/signing"
)

// Events: local producers (addons) publish to fairway; fairway fans each
// event out, as a signed webhook, to the subscriptions whose client holds
// both "events.subscribe:<topic>" and the event's own scope. Fairway never
// interprets topics, scopes or payloads — it compares strings.

// Event is one published event, also the webhook body.
type Event struct {
	ID         string    `json:"id"`
	Topic      string    `json:"topic"`
	OccurredAt time.Time `json:"occurred_at"`
	Source     string    `json:"source,omitempty"`
	// Scope, set by the producer, is what a subscriber must also hold to
	// receive this event (e.g. "whatsapp.read:<chat>"). Empty = topic
	// permission is enough.
	Scope string          `json:"scope,omitempty"`
	Data  json.RawMessage `json:"data"`
}

// Webhook headers added to the four signing headers.
const (
	HeaderEventID      = "X-Shipyard-Event-Id"
	HeaderEventTopic   = "X-Shipyard-Event-Topic"
	HeaderDeliveryTry  = "X-Shipyard-Delivery-Attempt"
	PingTopic          = "shipyard.ping"
	maxEventBytes      = 1 << 20
	webhookTimeout     = 10 * time.Second
	dispatchQueueSize  = 1024
	dispatchWorkers    = 4
	topicPermissionFmt = "events.subscribe:%s"
)

var topicRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,99}$`)

// ValidateTopic checks a published topic (no wildcards).
func ValidateTopic(t string) error {
	if !topicRe.MatchString(t) {
		return fmt.Errorf("topic %q: lower-case dotted words, e.g. whatsapp.message", t)
	}
	return nil
}

// DefaultBackoff is the wait before each retry of a failed webhook.
var DefaultWebhookBackoff = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second, 2 * time.Minute, 5 * time.Minute}

// ── Subscriptions ────────────────────────────────────────────────────────────

// Subscription sends a client the events of a topic (glob) at a URL.
type Subscription struct {
	ID        string    `json:"id"`
	ClientID  string    `json:"clientId"`
	Topic     string    `json:"topic"` // may contain '*'
	URL       string    `json:"url"`
	AllowHTTP bool      `json:"allowHttp,omitempty"` // admin-only escape for non-loopback http
	CreatedAt time.Time `json:"createdAt"`
}

// SubscriptionStats are live delivery counters (since daemon start).
type SubscriptionStats struct {
	Delivered     int64     `json:"delivered"`
	Failed        int64     `json:"failed"`
	LastStatus    int       `json:"lastStatus,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
	LastAttemptAt time.Time `json:"lastAttemptAt,omitzero"`
}

// SubscriptionView is a subscription with its stats, for listings.
type SubscriptionView struct {
	Subscription
	Stats SubscriptionStats `json:"stats"`
}

// Subscription errors.
var (
	ErrSubscriptionNotFound = errors.New("subscription not found")
	ErrInsecureWebhook      = errors.New("webhook URL must be https (http is accepted only for loopback hosts)")
)

var subTopicRe = regexp.MustCompile(`^[a-z*][a-z0-9_.*-]{0,99}$`)

// ValidateWebhookURL enforces https, except plain http to loopback (or
// anywhere when allowHTTP, an admin decision).
func ValidateWebhookURL(raw string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("webhook URL %q must be an absolute http(s) URL", raw)
	}
	if u.Scheme == "http" && !allowHTTP && !isLoopbackHost(u.Hostname()) {
		return ErrInsecureWebhook
	}
	return nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

type subscriptionsFile struct {
	SchemaVersion string         `json:"schemaVersion"`
	Subscriptions []Subscription `json:"subscriptions"`
}

// SubscriptionStore is the file-backed subscription registry.
type SubscriptionStore struct {
	path string
	now  func() time.Time
	mu   sync.RWMutex
	subs map[string]Subscription
}

// OpenSubscriptionStore loads path (missing = empty).
func OpenSubscriptionStore(path string) (*SubscriptionStore, error) {
	s := &SubscriptionStore{path: path, now: time.Now, subs: map[string]Subscription{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f subscriptionsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, sub := range f.Subscriptions {
		s.subs[sub.ID] = sub
	}
	return s, nil
}

// List returns subscriptions sorted by id; clientID filters when non-empty.
func (s *SubscriptionStore) List(clientID string) []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		if clientID == "" || sub.ClientID == clientID {
			out = append(out, sub)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get returns one subscription.
func (s *SubscriptionStore) Get(id string) (Subscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.subs[id]
	return sub, ok
}

// Add stores a validated subscription (the caller checks permissions).
func (s *SubscriptionStore) Add(sub Subscription) (Subscription, error) {
	if !subTopicRe.MatchString(sub.Topic) {
		return Subscription{}, fmt.Errorf("topic %q: lower-case dotted words, '*' allowed (e.g. whatsapp.message, whatsapp.*)", sub.Topic)
	}
	if err := ValidateWebhookURL(sub.URL, sub.AllowHTTP); err != nil {
		return Subscription{}, err
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	sub.ID = "sub_" + hex.EncodeToString(b[:])
	sub.CreatedAt = s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	return sub, s.commit(func(m map[string]Subscription) { m[sub.ID] = sub })
}

// Delete removes a subscription; clientID, when non-empty, must own it.
func (s *SubscriptionStore) Delete(id, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs[id]
	if !ok || (clientID != "" && sub.ClientID != clientID) {
		return fmt.Errorf("%w: %s", ErrSubscriptionNotFound, id)
	}
	return s.commit(func(m map[string]Subscription) { delete(m, id) })
}

// DeleteByClient removes every subscription of a client (client deleted).
func (s *SubscriptionStore) DeleteByClient(clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(func(m map[string]Subscription) {
		for id, sub := range m {
			if sub.ClientID == clientID {
				delete(m, id)
			}
		}
	})
}

func (s *SubscriptionStore) commit(change func(map[string]Subscription)) error {
	next := make(map[string]Subscription, len(s.subs)+1)
	for k, v := range s.subs {
		next[k] = v
	}
	change(next)
	f := subscriptionsFile{SchemaVersion: "1", Subscriptions: make([]Subscription, 0, len(next))}
	for _, sub := range next {
		f.Subscriptions = append(f.Subscriptions, sub)
	}
	sort.Slice(f.Subscriptions, func(i, j int) bool { return f.Subscriptions[i].ID < f.Subscriptions[j].ID })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile0600(s.path, append(data, '\n')); err != nil {
		return err
	}
	s.subs = next
	return nil
}

// ── Dispatcher ───────────────────────────────────────────────────────────────

type webhookJob struct {
	sub     Subscription
	ev      Event
	attempt int // 1-based
}

// Dispatcher fans events out and delivers webhooks. Delivery is
// at-least-once while the daemon runs (retries with backoff); the queue is
// in memory, so events pending at a restart are lost — consumers recover
// through the producer's own query API (cursor).
type Dispatcher struct {
	Subs    *SubscriptionStore
	Clients ClientLookup
	HTTP    *http.Client
	Log     *slog.Logger
	Now     func() time.Time
	Backoff []time.Duration
	Version string

	queue   chan webhookJob
	statsMu sync.Mutex
	stats   map[string]*SubscriptionStats
}

// NewDispatcher builds a dispatcher with production defaults.
func NewDispatcher(subs *SubscriptionStore, clients ClientLookup, log *slog.Logger, version string) *Dispatcher {
	if log == nil {
		log = slog.New(yardlogs.NopHandler())
	}
	return &Dispatcher{
		Subs: subs, Clients: clients, Log: log, Version: version,
		HTTP:    &http.Client{Timeout: webhookTimeout},
		Now:     time.Now,
		Backoff: DefaultWebhookBackoff,
		queue:   make(chan webhookJob, dispatchQueueSize),
		stats:   map[string]*SubscriptionStats{},
	}
}

// Run starts the delivery workers and blocks until ctx is done.
func (d *Dispatcher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < dispatchWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-d.queue:
					d.deliver(ctx, job)
				}
			}
		}()
	}
	wg.Wait()
}

// NewEventID returns a time-ordered unique event id.
func NewEventID(now time.Time) string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("evt_%013d%s", now.UnixMilli(), hex.EncodeToString(b[:]))
}

// eligible reports whether a subscription's client may receive ev now.
// Re-checked on every attempt: revoking a scope or a client stops
// deliveries at once.
func (d *Dispatcher) eligible(sub Subscription, ev Event) (Client, bool) {
	c, ok := d.Clients.Lookup(sub.ClientID)
	if !ok || c.Disabled {
		return Client{}, false
	}
	if ev.Topic == PingTopic {
		return c, true
	}
	if !signing.Grants(c.Scopes, fmt.Sprintf(topicPermissionFmt, ev.Topic)) {
		return Client{}, false
	}
	if ev.Scope != "" && !signing.Grants(c.Scopes, ev.Scope) {
		return Client{}, false
	}
	return c, true
}

// Publish fans ev out to matching subscriptions and returns how many were
// queued.
func (d *Dispatcher) Publish(ev Event) int {
	n := 0
	for _, sub := range d.Subs.List("") {
		if !signing.Glob(sub.Topic, ev.Topic) {
			continue
		}
		if _, ok := d.eligible(sub, ev); !ok {
			continue
		}
		if d.enqueue(webhookJob{sub: sub, ev: ev, attempt: 1}) {
			n++
		}
	}
	d.Log.Info(yardlogs.EventEventPublished, slog.String(yardlogs.KeyMessage, "event published"),
		slog.String(yardlogs.KeyEventTopic, ev.Topic), slog.String(yardlogs.KeyEventID, ev.ID), slog.Int(yardlogs.KeySubscribers, n))
	return n
}

// Ping queues a test event for one subscription.
func (d *Dispatcher) Ping(subID string) (Event, error) {
	sub, ok := d.Subs.Get(subID)
	if !ok {
		return Event{}, fmt.Errorf("%w: %s", ErrSubscriptionNotFound, subID)
	}
	now := d.Now().UTC()
	ev := Event{ID: NewEventID(now), Topic: PingTopic, OccurredAt: now, Source: "fairway", Data: json.RawMessage(`{"ping":true}`)}
	if !d.enqueue(webhookJob{sub: sub, ev: ev, attempt: 1}) {
		return Event{}, errors.New("delivery queue full")
	}
	return ev, nil
}

func (d *Dispatcher) enqueue(job webhookJob) bool {
	select {
	case d.queue <- job:
		return true
	default:
		d.record(job.sub.ID, 0, "delivery queue full; event dropped", false)
		return false
	}
}

func (d *Dispatcher) deliver(ctx context.Context, job webhookJob) {
	c, ok := d.eligible(job.sub, job.ev)
	if !ok {
		return // client gone, disabled or lost the scope since publish
	}
	status, err := d.post(ctx, c, job)
	if err == nil {
		d.record(job.sub.ID, status, "", true)
		return
	}
	retryable := status == 0 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
	if retryable && job.attempt <= len(d.Backoff) {
		d.record(job.sub.ID, status, err.Error(), false)
		wait := d.Backoff[job.attempt-1]
		next := webhookJob{sub: job.sub, ev: job.ev, attempt: job.attempt + 1}
		time.AfterFunc(wait, func() {
			if ctx.Err() == nil {
				d.enqueue(next)
			}
		})
		return
	}
	d.record(job.sub.ID, status, err.Error(), false)
	d.Log.Warn(yardlogs.EventWebhookFailed, slog.String(yardlogs.KeyMessage, "webhook not delivered"),
		slog.String(yardlogs.KeyEntityID, job.sub.ID), slog.String(yardlogs.KeyClientID, job.sub.ClientID),
		slog.String(yardlogs.KeyEventID, job.ev.ID), slog.Int(yardlogs.KeyHTTPStatus, status),
		slog.Int(yardlogs.KeyAttempts, job.attempt), slog.String(yardlogs.KeyError, err.Error()))
}

func (d *Dispatcher) post(ctx context.Context, c Client, job webhookJob) (int, error) {
	body, err := json.Marshal(job.ev)
	if err != nil {
		return 0, err
	}
	u, err := url.Parse(job.sub.URL)
	if err != nil {
		return 0, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, job.sub.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	// Signed with the subscriber's own secret: it verifies with the same
	// code it uses to sign requests to us.
	// The scheme signs the decoded path (as servers see r.URL.Path); a URL
	// without a path is requested as "/".
	path := u.Path
	if path == "" {
		path = "/"
	}
	for k, v := range signing.SignRequest(c.ID, c.Secret, http.MethodPost, path, u.RawQuery, body, d.Now()) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "shipyard-fairway/"+d.Version)
	req.Header.Set(HeaderEventID, job.ev.ID)
	req.Header.Set(HeaderEventTopic, job.ev.Topic)
	req.Header.Set(HeaderDeliveryTry, strconv.Itoa(job.attempt))
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("subscriber answered %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func (d *Dispatcher) record(subID string, status int, errMsg string, ok bool) {
	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	st := d.stats[subID]
	if st == nil {
		st = &SubscriptionStats{}
		d.stats[subID] = st
	}
	st.LastAttemptAt = d.Now().UTC()
	st.LastStatus = status
	st.LastError = errMsg
	if ok {
		st.Delivered++
	} else if errMsg != "" {
		st.Failed++
	}
}

// Views lists subscriptions with their stats; clientID filters.
func (d *Dispatcher) Views(clientID string) []SubscriptionView {
	subs := d.Subs.List(clientID)
	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	out := make([]SubscriptionView, 0, len(subs))
	for _, sub := range subs {
		v := SubscriptionView{Subscription: sub}
		if st := d.stats[sub.ID]; st != nil {
			v.Stats = *st
		}
		out = append(out, v)
	}
	return out
}

// Subscribe adds a subscription after checking the client may receive the
// topic (a glob subscription needs the permission for the glob itself,
// e.g. events.subscribe:whatsapp.* — or a broader grant).
func (d *Dispatcher) Subscribe(clientID, topic, rawURL string, allowHTTP bool) (Subscription, error) {
	c, ok := d.Clients.Lookup(clientID)
	if !ok {
		return Subscription{}, fmt.Errorf("%w: %s", ErrClientNotFound, clientID)
	}
	if !signing.Grants(c.Scopes, fmt.Sprintf(topicPermissionFmt, topic)) {
		return Subscription{}, fmt.Errorf("client %s lacks scope events.subscribe:%s", clientID, topic)
	}
	return d.Subs.Add(Subscription{ClientID: clientID, Topic: topic, URL: rawURL, AllowHTTP: allowHTTP})
}
