package wa

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
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// Event is what an inbound message looks like on the wire to fairway: the
// comms Message envelope, schema v1 (addons/comms/internal/comms/envelope.go).
// It is re-declared here because addons must not import each other's
// internals; the JSON shape is the contract. WhatsApp specifics ride in
// Metadata so the envelope version does not change.
type Event struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	TraceID       string            `json:"trace_id"`
	Channel       string            `json:"channel"`
	Direction     string            `json:"direction"`
	From          EventAddress      `json:"from"`
	To            []EventAddress    `json:"to"`
	Body          string            `json:"body"`
	Metadata      map[string]string `json:"metadata"`
	Timestamp     time.Time         `json:"ts"`
}

// EventAddress is comms.Address.
type EventAddress struct {
	Channel string `json:"channel"`
	Raw     string `json:"raw"`
	Display string `json:"display,omitempty"`
}

// EventChannel is the channel id inbound events carry.
const EventChannel = "whatsapp"

// Delivery tuning.
const (
	DeliveryQueueSize = 256
	DeliveryTimeout   = 15 * time.Second
)

// DefaultBackoff is the wait before each retry of a failed delivery.
var DefaultBackoff = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

type deliveryJob struct {
	path    string
	body    []byte // marshalled payload
	traceID string
	chatJID string // for logs only
}

// EventsPath is fairway's publish endpoint (loopback only).
const EventsPath = "/_shipyard/v1/events"

// EventTopic is the topic received messages are published under.
const EventTopic = "whatsapp.message"

// PublishedEvent is the envelope fairway's events API accepts. Scope
// declares what a subscriber must hold to receive it: reading this chat.
type PublishedEvent struct {
	ID         string    `json:"id"`
	Topic      string    `json:"topic"`
	Scope      string    `json:"scope"`
	Source     string    `json:"source"`
	OccurredAt time.Time `json:"occurred_at"`
	Data       Event     `json:"data"`
}

// ReadScope is the scope a client needs to receive a chat's messages.
func ReadScope(chatJID string) string { return "whatsapp.read:" + chatJID }

// Deliverer posts events to fairway from one background worker, so the
// WhatsApp event loop never waits on HTTP. A full queue drops the event
// (logged); watched chats still have the message in the archive.
type Deliverer struct {
	Client  *http.Client
	Addr    func() string // fairway "host:port", resolved per delivery
	Log     *slog.Logger
	Backoff []time.Duration
	Sleep   func(ctx context.Context, d time.Duration) error

	queue     chan deliveryJob
	delivered atomic.Int64
	failed    atomic.Int64
}

// NewDeliverer returns a Deliverer with production defaults.
func NewDeliverer(addr func() string, log *slog.Logger) *Deliverer {
	return &Deliverer{
		Client:  &http.Client{Timeout: DeliveryTimeout},
		Addr:    addr,
		Log:     log,
		Backoff: DefaultBackoff,
		Sleep:   SleepCtx,
		queue:   make(chan deliveryJob, DeliveryQueueSize),
	}
}

// Enqueue schedules ev for path without blocking. It reports false when
// the queue is full and the event was dropped.
func (d *Deliverer) Enqueue(path string, ev Event) bool {
	body, err := json.Marshal(ev)
	if err != nil {
		return false
	}
	return d.enqueue(deliveryJob{path: path, body: body, traceID: ev.TraceID, chatJID: ev.Metadata["chat_jid"]})
}

// Publish sends ev to fairway's events API as a whatsapp.message event
// scoped to its chat, so only clients allowed to read that chat get it.
// The event id derives from the message id: a redelivered message keeps
// its id and subscribers deduplicate it.
func (d *Deliverer) Publish(ev Event) bool {
	chat := ev.Metadata["chat_jid"]
	body, err := json.Marshal(PublishedEvent{
		ID: "evt_wa_" + ev.ID, Topic: EventTopic, Scope: ReadScope(chat), Source: "comms-whatsapp",
		OccurredAt: ev.Timestamp, Data: ev,
	})
	if err != nil {
		return false
	}
	return d.enqueue(deliveryJob{path: EventsPath, body: body, traceID: ev.TraceID, chatJID: chat})
}

// PublishMembers sends a membership change as a whatsapp.group.members
// event scoped to its group's membership: whatsapp.read does not receive
// it. The envelope id is the event id, so retries dedupe.
func (d *Deliverer) PublishMembers(ev MemberEvent) bool {
	body, err := json.Marshal(struct {
		ID         string      `json:"id"`
		Topic      string      `json:"topic"`
		Scope      string      `json:"scope"`
		Source     string      `json:"source"`
		OccurredAt time.Time   `json:"occurred_at"`
		Data       MemberEvent `json:"data"`
	}{ev.ID, MembersEventTopic, MembersScope(ev.ChatJID), "comms-whatsapp", ev.Timestamp, ev})
	if err != nil {
		return false
	}
	return d.enqueue(deliveryJob{path: EventsPath, body: body, traceID: newTraceID(), chatJID: ev.ChatJID})
}

func (d *Deliverer) enqueue(job deliveryJob) bool {
	select {
	case d.queue <- job:
		return true
	default:
		d.failed.Add(1)
		d.Log.Error(yardlogs.EventWADeliveryFailed, slog.String(yardlogs.KeyMessage, "delivery queue full; event dropped"),
			slog.String(yardlogs.KeyChatJID, job.chatJID), slog.String(yardlogs.KeyRouteTarget, job.path))
		return false
	}
}

// Counts returns how many events were delivered and how many failed
// (dropped or out of retries) since start.
func (d *Deliverer) Counts() (delivered, failed int64) {
	return d.delivered.Load(), d.failed.Load()
}

// Run drains the queue until ctx is done. Events still queued at shutdown
// are dropped: they were in memory only by design.
func (d *Deliverer) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-d.queue:
			if err := d.deliver(ctx, job); err != nil {
				d.failed.Add(1)
				d.Log.Error(yardlogs.EventWADeliveryFailed, slog.String(yardlogs.KeyMessage, "event not delivered to fairway"),
					slog.String(yardlogs.KeyChatJID, job.chatJID), slog.String(yardlogs.KeyRouteTarget, job.path),
					slog.String(yardlogs.KeyTraceID, job.traceID), slog.String(yardlogs.KeyError, err.Error()))
				continue
			}
			d.delivered.Add(1)
		}
	}
}

// errPermanent marks a failure that retrying cannot fix (a 4xx: wrong
// path, auth, bad payload).
type errPermanent struct{ error }

func (d *Deliverer) deliver(ctx context.Context, job deliveryJob) error {
	body := job.body
	var lastErr error
	for attempt := 0; ; attempt++ {
		lastErr = d.post(ctx, job, body)
		var perm errPermanent
		if lastErr == nil || errors.As(lastErr, &perm) || attempt >= len(d.Backoff) {
			return lastErr
		}
		if err := d.Sleep(ctx, d.Backoff[attempt]); err != nil {
			return fmt.Errorf("%w (shutting down)", lastErr)
		}
	}
}

func (d *Deliverer) post(ctx context.Context, job deliveryJob, body []byte) error {
	url := "http://" + d.Addr() + job.path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errPermanent{err}
	}
	req.Header.Set("Content-Type", "application/json")
	// Fairway adopts this id, so the delivery, the crew run it triggers and
	// any reply share one trace in `shipyard logs show --trace`.
	req.Header.Set("X-Trace-Id", job.traceID)
	resp, err := d.Client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("fairway answered %d", resp.StatusCode)
	default:
		return errPermanent{fmt.Errorf("fairway answered %d for %s (check the route with 'shipyard fairway route list')", resp.StatusCode, job.path)}
	}
}

// FairwayAddr resolves where events go: the configured override, else
// bind/port from fairway's own routes.json, else 127.0.0.1:9876. fairwayDir
// is $SHIPYARD_HOME/fairway.
func FairwayAddr(override, fairwayDir string) string {
	if override != "" {
		return override
	}
	host, port := "127.0.0.1", 9876
	if raw, err := os.ReadFile(filepath.Join(fairwayDir, "routes.json")); err == nil {
		var f struct {
			Port int    `json:"port"`
			Bind string `json:"bind"`
		}
		if json.Unmarshal(raw, &f) == nil {
			if f.Port > 0 {
				port = f.Port
			}
			// A wildcard bind is reachable on loopback.
			if f.Bind != "" && f.Bind != "0.0.0.0" && f.Bind != "::" {
				host = f.Bind
			}
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func newTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
