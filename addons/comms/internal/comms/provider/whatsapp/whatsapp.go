// Package whatsapp implements the comms provider for WhatsApp. Unlike
// Telegram it holds no credentials and makes no network call: WhatsApp
// needs a persistent linked-device connection, which lives in the separate
// shipyard-comms-whatsapp daemon. This provider is a thin client of that
// daemon's control socket, so `shipyard comms send --channel wa` and the
// daemon's own CLI go through the same send policy (allowlist, prior
// conversation, rate limits, pause).
//
// Contract with shipyard-comms-whatsapp (the only coupling between the two
// binaries): a unix socket at <comms home>/whatsapp/run/wa.sock speaking
// line-delimited JSON-RPC 2.0; method "send" takes {chat, text,
// idempotency_key?} and returns {msg_id, sent_at, …}; refusals carry
// error.data.code (not_allowed, no_history, rate_limited, paused, …).
package whatsapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms/provider"
)

// MetadataIdempotencyKey is the Message.Metadata key whose value, when
// present, is forwarded as the daemon's idempotency key: a retried send
// with the same key returns the first result instead of sending twice.
const MetadataIdempotencyKey = "idempotency_key"

// SettingSocket optionally overrides the socket path per channel.
const SettingSocket = "socket"

// callTimeout covers the daemon's "typing…" delay (≤8s) plus a slow send.
const callTimeout = 60 * time.Second

// RefusedError is a send the daemon's policy refused. Code is stable;
// callers branch on it.
type RefusedError struct {
	Code    string
	Message string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("whatsapp: refused (%s): %s", e.Code, e.Message)
}

// ErrDaemonNotRunning means nothing listens on the socket.
var ErrDaemonNotRunning = errors.New("whatsapp: the shipyard-comms-whatsapp daemon is not running (check 'shipyard comms whatsapp status')")

// Provider implements provider.Provider for WhatsApp.
type Provider struct {
	// SocketPath returns the default socket location. Nil means
	// <comms home>/whatsapp/run/wa.sock.
	SocketPath func() (string, error)
}

// New returns a Provider using the default socket location.
func New() *Provider { return &Provider{} }

// Name returns "whatsapp" — matches comms.ChannelTypeWhatsApp.
func (p *Provider) Name() string { return "whatsapp" }

// Send asks the daemon to deliver msg.Body to msg.To[0]. The recipient is
// a JID, a phone number (normalized here) or a chat name (resolved by the
// daemon).
func (p *Provider) Send(ctx context.Context, ch comms.Channel, _ []byte, msg comms.Message) (provider.Receipt, error) {
	if len(msg.To) != 1 {
		return provider.Receipt{}, fmt.Errorf("whatsapp: send accepts exactly 1 recipient, got %d", len(msg.To))
	}
	to := strings.TrimSpace(msg.To[0].Raw)
	if to == "" {
		return provider.Receipt{}, errors.New("whatsapp: to.raw (JID, phone number or chat name) is required")
	}
	if msg.Body == "" {
		return provider.Receipt{}, errors.New("whatsapp: message body is required (media not supported)")
	}
	sock, err := p.socketFor(ch)
	if err != nil {
		return provider.Receipt{}, err
	}

	params := map[string]any{"chat": NormalizeRecipient(to), "text": msg.Body}
	if key := msg.Metadata[MetadataIdempotencyKey]; key != "" {
		params["idempotency_key"] = key
	}
	var res struct {
		MsgID  string    `json:"msg_id"`
		SentAt time.Time `json:"sent_at"`
	}
	if err := call(ctx, sock, "send", params, &res); err != nil {
		return provider.Receipt{}, err
	}
	return provider.Receipt{ProviderMessageID: res.MsgID, SentAt: res.SentAt}, nil
}

func (p *Provider) socketFor(ch comms.Channel) (string, error) {
	if s, ok := ch.Settings[SettingSocket].(string); ok && s != "" {
		return s, nil
	}
	if p.SocketPath != nil {
		return p.SocketPath()
	}
	home, err := comms.CommsHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "whatsapp", "run", "wa.sock"), nil
}

// NormalizeRecipient turns a phone number in any common notation into a
// WhatsApp JID ("+55 (11) 99999-9999" → "5511999999999@s.whatsapp.net").
// JIDs and names pass through untouched.
func NormalizeRecipient(to string) string {
	if strings.Contains(to, "@") {
		return to
	}
	var digits strings.Builder
	for _, r := range to {
		switch {
		case unicode.IsDigit(r):
			digits.WriteRune(r)
		case strings.ContainsRune("+ -().", r):
		default:
			return to // a name
		}
	}
	d := digits.String()
	if len(d) < 8 || len(d) > 15 || strings.HasPrefix(d, "0") {
		return to
	}
	return d + "@s.whatsapp.net"
}

// call performs one JSON-RPC request over the unix socket.
func call(ctx context.Context, sock, method string, params, out any) error {
	if _, err := os.Stat(sock); errors.Is(err, os.ErrNotExist) {
		return ErrDaemonNotRunning
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return ErrDaemonNotRunning
		}
		return fmt.Errorf("whatsapp: dial daemon: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("whatsapp: write request: %w", err)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return fmt.Errorf("whatsapp: read response: %w", err)
		}
		return errors.New("whatsapp: daemon closed the connection without answering")
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    *struct {
				Code string `json:"code"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return fmt.Errorf("whatsapp: decode response: %w", err)
	}
	if e := resp.Error; e != nil {
		if e.Data != nil && e.Data.Code != "" {
			return &RefusedError{Code: e.Data.Code, Message: e.Message}
		}
		return fmt.Errorf("whatsapp: daemon error %d: %s", e.Code, e.Message)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("whatsapp: decode result: %w", err)
		}
	}
	return nil
}
