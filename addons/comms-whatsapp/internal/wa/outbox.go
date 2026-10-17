package wa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// SendRequest is one outgoing text message.
type SendRequest struct {
	// Chat is a catalog JID, chat name (case/accent-insensitive) or phone
	// number; resolved by the daemon so every client addresses chats alike.
	Chat string `json:"chat"`
	Text string `json:"text"`
	// IdempotencyKey, when set, makes retries safe: a second request with
	// the same key returns the first result instead of sending again.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// DryRun runs every check without sending.
	DryRun bool `json:"dry_run,omitempty"`
	// Client is the external system behind the request, set by the query
	// API from fairway's identity and recorded in the audit log. Never
	// read from the socket: local callers cannot claim to be a client.
	Client string `json:"-"`
}

// SendResult describes a send that happened (or would have, for DryRun).
type SendResult struct {
	MsgID     string    `json:"msg_id,omitempty"`
	Chat      string    `json:"chat"`
	ChatName  string    `json:"chat_name"`
	SentAt    time.Time `json:"sent_at,omitzero"`
	Duplicate bool      `json:"duplicate,omitempty"` // replayed from an idempotency key
	DryRun    bool      `json:"dry_run,omitempty"`
}

// SendCode classifies why a send was refused or failed. Codes are part of
// the socket and CLI contract: agents branch on them.
type SendCode string

const (
	CodeInvalid     SendCode = "invalid"
	CodeUnknownChat SendCode = "unknown_chat"
	CodePaused      SendCode = "paused"
	CodeNotAllowed  SendCode = "not_allowed"
	// CodeGroupsDisabled: the chat is a group and allow_groups is off.
	CodeGroupsDisabled SendCode = "groups_disabled"
	CodeNoHistory      SendCode = "no_history"
	CodeRateLimited    SendCode = "rate_limited"
	CodeTooLong        SendCode = "too_long"
	CodeNotConnected   SendCode = "not_connected"
	CodeSendFailed     SendCode = "send_failed"
)

// SendError is a refused or failed send.
type SendError struct {
	Code SendCode
	Msg  string
	// RetryAfter, for rate_limited, is how long until a slot opens.
	RetryAfter time.Duration
}

func (e *SendError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Msg) }

func refuse(code SendCode, format string, args ...any) *SendError {
	return &SendError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Sender is the transport the Outbox drives. An interface so the policy can
// be tested without WhatsApp.
type Sender interface {
	Connected() bool
	// Typing toggles the "typing…" indicator in a chat.
	Typing(ctx context.Context, chat types.JID, on bool) error
	SendText(ctx context.Context, chat types.JID, text string) (id string, at time.Time, err error)
}

// Outbox is the only path a message takes out of this machine. Every check
// lives here, behind the daemon's socket, so no caller — CLI, crew tool or
// a misbehaving agent with a shell — can skip it.
type Outbox struct {
	Archive *archive.Store
	Sender  Sender
	// Config is re-read on every send so `pause` and limit changes apply
	// without restarting the daemon.
	Config func() (Config, error)
	// Self is this account's phone-number JID, recorded as the sender of
	// archived outgoing messages.
	Self func() types.JID
	Log  *slog.Logger
	Now  func() time.Time
	// Sleep waits for d or until ctx ends. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error

	// mu serializes sends. Rate limits are check-then-act against the
	// audit log, and sends are rare enough that one at a time costs
	// nothing — far simpler than per-chat locking.
	mu sync.Mutex
}

// TypingDelay is how long "typing…" shows before a message of n characters.
func TypingDelay(n int) time.Duration {
	d := 1500*time.Millisecond + time.Duration(n)*40*time.Millisecond
	return min(d, 8*time.Second)
}

// Send validates req against the policy and, unless DryRun, sends it.
func (o *Outbox) Send(ctx context.Context, req SendRequest) (SendResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	res, err := o.send(ctx, req)
	if err != nil {
		var se *SendError
		code := string(CodeSendFailed)
		if errors.As(err, &se) {
			code = string(se.Code)
		}
		event := yardlogs.EventWASendDenied
		if code == string(CodeSendFailed) || code == string(CodeNotConnected) {
			event = yardlogs.EventWASendFailed
		}
		o.Log.Warn(event, slog.String(yardlogs.KeyMessage, "send refused or failed"),
			slog.String(yardlogs.KeyChatJID, req.Chat), slog.String(yardlogs.KeyEntityID, req.Client), slog.String(yardlogs.KeyErrorKind, code),
			slog.String(yardlogs.KeyError, err.Error()))
	}
	return res, err
}

func (o *Outbox) send(ctx context.Context, req SendRequest) (SendResult, error) {
	chat, err := ResolveChat(ctx, o.Archive, req.Chat)
	var amb *archive.AmbiguousError
	switch {
	case errors.As(err, &amb):
		return SendResult{}, refuse(CodeInvalid, "%v", amb)
	case errors.Is(err, archive.ErrNotFound):
		return SendResult{}, refuse(CodeUnknownChat, "%q is not in the catalog (see 'chats --search')", req.Chat)
	case err != nil:
		return SendResult{}, err
	}
	jid, err := types.ParseJID(chat.JID)
	if err != nil || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.GroupServer) {
		return SendResult{}, refuse(CodeInvalid, "chat %q is not a user or group JID", chat.JID)
	}

	if req.IdempotencyKey != "" {
		prev, ok, err := o.Archive.SentByKey(ctx, req.IdempotencyKey)
		if err != nil {
			return SendResult{}, err
		}
		if ok {
			if prev.ChatJID != chat.JID {
				return SendResult{}, refuse(CodeInvalid, "idempotency key %q was already used for another chat", req.IdempotencyKey)
			}
			return SendResult{MsgID: prev.MsgID, Chat: prev.ChatJID, ChatName: chat.Name, SentAt: prev.Timestamp, Duplicate: true}, nil
		}
	}

	cfg, err := o.Config()
	if err != nil {
		return SendResult{}, err
	}
	text := req.Text
	switch {
	case strings.TrimSpace(text) == "":
		return SendResult{}, refuse(CodeInvalid, "text is empty")
	case !utf8.ValidString(text):
		return SendResult{}, refuse(CodeInvalid, "text is not valid UTF-8")
	case utf8.RuneCountInString(text) > cfg.MaxChars:
		return SendResult{}, refuse(CodeTooLong, "text has %d characters; the limit is %d", utf8.RuneCountInString(text), cfg.MaxChars)
	}
	result := SendResult{Chat: chat.JID, ChatName: chat.Name}

	if cfg.Paused {
		return result, refuse(CodePaused, "sending is paused ('resume' to lift)")
	}
	group := chat.Kind == archive.KindGroup
	if group && !cfg.AllowGroups {
		return result, refuse(CodeGroupsDisabled, "%s is a group and sending to groups is off ('config set allow-groups true' to enable)", chat.Name)
	}
	if !chat.SendAllowed {
		return result, refuse(CodeNotAllowed, "%s is not on the send allowlist ('allow %s' to add it)", chat.Name, chat.JID)
	}
	if chat.LastActivity.IsZero() {
		return result, refuse(CodeNoHistory,
			"no conversation with %s has been seen yet; send or receive a message there from the phone first", chat.Name)
	}
	now := o.Now()
	limits := cfg.ParsedLimits()
	scope := "per-chat"
	if group {
		scope = "per-group"
	}
	for _, check := range []struct {
		scope, chat, label string
		rate               Rate
	}{
		{scope, chat.JID, "to " + chat.Name, limits.ForKind(group)},
		{"global", "", "across all chats", limits.Global},
	} {
		u, err := usage(ctx, o.Archive, check.scope, check.chat, check.rate, now)
		if err != nil {
			return result, err
		}
		if u.Remaining == 0 {
			se := refuse(CodeRateLimited, "%d messages %s in the last %s (limit: %s); next slot at %s",
				u.Used, check.label, FormatDuration(check.rate.Window), check.rate.Human(), u.NextFreeAt.Local().Format("15:04:05"))
			se.RetryAfter = time.Duration(u.RetryAfterSeconds) * time.Second
			return result, se
		}
	}

	if req.DryRun {
		result.DryRun = true
		return result, nil
	}
	if !o.Sender.Connected() {
		return result, refuse(CodeNotConnected, "the daemon is not connected to WhatsApp right now")
	}

	if cfg.Humanize {
		// A failed typing indicator is cosmetic; never let it block a send.
		_ = o.Sender.Typing(ctx, jid, true)
		if err := o.Sleep(ctx, TypingDelay(utf8.RuneCountInString(text))); err != nil {
			_ = o.Sender.Typing(context.WithoutCancel(ctx), jid, false)
			return result, err
		}
		_ = o.Sender.Typing(ctx, jid, false)
	}

	id, at, err := o.Sender.SendText(ctx, jid, text)
	if err != nil {
		return result, refuse(CodeSendFailed, "%v", err)
	}
	result.MsgID, result.SentAt = id, at.UTC()

	// The message is out; from here on, failures are logged, not returned,
	// so a caller never retries (and duplicates) a message that was sent.
	bg := context.WithoutCancel(ctx)
	if err := o.Archive.RecordSent(bg, archive.Sent{
		MsgID: id, ChatJID: chat.JID, Timestamp: at, Chars: utf8.RuneCountInString(text), IdempotencyKey: req.IdempotencyKey,
		Client: req.Client,
	}); err != nil {
		o.logArchiveError(chat.JID, err)
	}
	if _, err := o.Archive.InsertMessage(bg, archive.Message{
		ChatJID: chat.JID, ID: id, SenderJID: o.Self().String(), FromMe: true,
		Timestamp: at, Kind: "text", Text: text, ViaShipyard: true,
	}); err != nil {
		o.logArchiveError(chat.JID, err)
	}
	if err := o.Archive.TouchActivity(bg, chat.JID, at); err != nil {
		o.logArchiveError(chat.JID, err)
	}
	o.Log.Info(yardlogs.EventWAMessageSent, slog.String(yardlogs.KeyMessage, "message sent"),
		slog.String(yardlogs.KeyChatJID, chat.JID), slog.String(yardlogs.KeyEntityID, req.Client),
		slog.Int(yardlogs.KeyCount, utf8.RuneCountInString(text)))
	return result, nil
}

func (o *Outbox) logArchiveError(chatJID string, err error) {
	o.Log.Error(yardlogs.EventWAArchiveError, slog.String(yardlogs.KeyMessage, "recording a sent message failed"),
		slog.String(yardlogs.KeyChatJID, chatJID), slog.String(yardlogs.KeyError, err.Error()))
}

// SleepCtx waits for d or until ctx is done.
func SleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// clientSender adapts *whatsmeow.Client to Sender.
type clientSender struct{ cli *whatsmeow.Client }

func (c clientSender) Connected() bool { return c.cli.IsConnected() && c.cli.IsLoggedIn() }

func (c clientSender) Typing(ctx context.Context, chat types.JID, on bool) error {
	state := types.ChatPresencePaused
	if on {
		state = types.ChatPresenceComposing
	}
	return c.cli.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText)
}

func (c clientSender) SendText(ctx context.Context, chat types.JID, text string) (string, time.Time, error) {
	resp, err := c.cli.SendMessage(ctx, chat, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return "", time.Time{}, err
	}
	return resp.ID, resp.Timestamp, nil
}

// Sender returns the whatsmeow-backed Sender for this session.
func (s *Session) Sender() Sender { return clientSender{cli: s.Client} }
