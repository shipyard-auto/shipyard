package wa

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// The query API lets external systems read the archive through fairway:
//
//	client ──signed──▶ fairway route (auth signed, scope whatsapp.read)
//	       ──http.forward + api token──▶ this API (loopback only)
//
// Fairway authenticates the client and forwards its identity in
// X-Shipyard-Client-Id / -Scopes; the API token proves the request came
// from fairway (it lives in the route definition, 0600), and this API
// enforces the per-chat resource of whatsapp.read itself. Fairway stays
// ignorant of chats: to it the route is an opaque scope.

// API endpoints (POST, JSON body).
const (
	APIMessagesPath = "/v1/messages"
	APIChatsPath    = "/v1/chats"
)

// Query limits.
const (
	APIDefaultLimit = 100
	APIMaxLimit     = 500
	apiBatch        = 500
	apiMaxBody      = 64 << 10
	apiTokenSize    = 32
)

// apiMaxScan bounds the rows one request reads while skipping chats the
// client cannot see; has_more is then true with a fresh cursor. A variable
// so tests can lower it.
var apiMaxScan = 5000

// Identity headers set by fairway on forwarded requests. Mirrors fairway's
// HeaderClientID/HeaderClientScopes (the header names are the contract).
const (
	headerClientID     = "X-Shipyard-Client-Id"
	headerClientScopes = "X-Shipyard-Client-Scopes"
)

// APIServer serves the query API.
type APIServer struct {
	Archive *archive.Store
	// Token returns the shared secret fairway sends as a bearer token.
	Token func() (string, error)
	// Self is the account's own phone JID, for the envelopes' "to".
	Self func() types.JID
	Log  *slog.Logger
}

// Handler returns the HTTP handler.
func (s *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(APIMessagesPath, s.guard(s.messages))
	mux.HandleFunc(APIChatsPath, s.guard(s.chats))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		apiError(w, http.StatusNotFound, "not_found", "unknown endpoint; use POST "+APIMessagesPath+" or "+APIChatsPath)
	})
	return mux
}

// apiCaller is the client fairway authenticated.
type apiCaller struct {
	ID     string
	Scopes []string
}

func (c apiCaller) canRead(chatJID string) bool {
	return grants(c.Scopes, ReadScopeName+":"+chatJID)
}

type apiHandler func(w http.ResponseWriter, r *http.Request, c apiCaller, body []byte)

// guard checks method, the fairway token and the forwarded identity.
func (s *APIServer) guard(next apiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST with a JSON body")
			return
		}
		want, err := s.Token()
		if err != nil || want == "" {
			apiError(w, http.StatusServiceUnavailable, "unavailable", "API token missing; run 'api enable'")
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			s.denied(r, "", "bad api token")
			apiError(w, http.StatusUnauthorized, "unauthorized", "invalid API token: requests must come through the fairway route")
			return
		}
		c := apiCaller{ID: r.Header.Get(headerClientID)}
		for _, sc := range strings.Split(r.Header.Get(headerClientScopes), ",") {
			if sc = strings.TrimSpace(sc); sc != "" {
				c.Scopes = append(c.Scopes, sc)
			}
		}
		if c.ID == "" {
			s.denied(r, "", "no client identity")
			apiError(w, http.StatusForbidden, "forbidden", "no client identity: the fairway route must use --auth signed")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, apiMaxBody+1))
		if err != nil || len(body) > apiMaxBody {
			apiError(w, http.StatusBadRequest, "invalid", "unreadable or oversized body")
			return
		}
		next(w, r, c, body)
	}
}

// MessagesRequest is the body of POST /v1/messages.
type MessagesRequest struct {
	// Chat narrows to one chat: JID, name or phone, like the CLI.
	Chat string `json:"chat,omitempty"`
	// After is next_cursor from a previous response; empty = from the start.
	After string `json:"after,omitempty"`
	// Since skips messages older than this: a duration ("24h", "7d") or
	// an RFC 3339 time. Typical for a first call without a cursor.
	Since string `json:"since,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// MessagesResponse is one page of archived messages, oldest arrival first.
type MessagesResponse struct {
	Messages []Event `json:"messages"`
	// NextCursor resumes after the last message read (store it even when
	// Messages is empty: skipped rows advance it too). Empty only when
	// nothing has been read yet.
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

func (s *APIServer) messages(w http.ResponseWriter, r *http.Request, c apiCaller, body []byte) {
	var req MessagesRequest
	if err := decodeBody(body, &req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	limit := req.Limit
	switch {
	case limit == 0:
		limit = APIDefaultLimit
	case limit < 0 || limit > APIMaxLimit:
		apiError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("limit must be 1-%d", APIMaxLimit))
		return
	}
	after, err := decodeCursor(req.After)
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	since, err := parseSince(req.Since, time.Now())
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	q := archive.FeedQuery{After: after, Since: since}
	if req.Chat != "" {
		chat, ok := s.resolveReadable(w, r, c, req.Chat)
		if !ok {
			return
		}
		q.Chat = chat.JID
	}

	resp := MessagesResponse{Messages: []Event{}}
	self := ""
	if s.Self != nil {
		self = s.Self().String()
	}
	cursor, scanned := after, 0
scan:
	for {
		q.After, q.Limit = cursor, apiBatch
		rows, err := s.Archive.Feed(r.Context(), q)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "internal", "archive read failed")
			s.Log.Error(yardlogs.EventWAArchiveError, slog.String(yardlogs.KeyMessage, "api feed read failed"), slog.String(yardlogs.KeyError, err.Error()))
			return
		}
		for _, row := range rows {
			if len(resp.Messages) == limit {
				resp.HasMore = true
				break scan
			}
			if scanned == apiMaxScan {
				resp.HasMore = true
				break scan
			}
			cursor, scanned = row.Seq, scanned+1
			if c.canRead(row.ChatJID) {
				resp.Messages = append(resp.Messages, archivedEvent(row, self))
			}
		}
		if len(rows) < apiBatch {
			break
		}
	}
	if cursor > 0 {
		resp.NextCursor = encodeCursor(cursor)
	}
	s.Log.Info(yardlogs.EventWAAPIQuery, slog.String(yardlogs.KeyMessage, "api messages query"),
		slog.String(yardlogs.KeyEntityID, c.ID), slog.String(yardlogs.KeyChatJID, q.Chat),
		slog.Int(yardlogs.KeyCount, len(resp.Messages)))
	apiJSON(w, http.StatusOK, resp)
}

// ChatsRequest is the body of POST /v1/chats.
type ChatsRequest struct {
	Search      string `json:"search,omitempty"`
	Kind        string `json:"kind,omitempty"` // dm|group|self
	WatchedOnly bool   `json:"watched_only,omitempty"`
}

// ChatsResponse lists the chats the client may read.
type ChatsResponse struct {
	Chats []archive.Chat `json:"chats"`
}

func (s *APIServer) chats(w http.ResponseWriter, r *http.Request, c apiCaller, body []byte) {
	var req ChatsRequest
	if err := decodeBody(body, &req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	switch archive.ChatKind(req.Kind) {
	case "", archive.KindDM, archive.KindGroup, archive.KindSelf:
	default:
		apiError(w, http.StatusBadRequest, "invalid", "kind must be dm, group or self")
		return
	}
	all, err := s.Archive.ListChats(r.Context(), archive.ListFilter{Search: req.Search, WatchedOnly: req.WatchedOnly})
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal", "archive read failed")
		return
	}
	resp := ChatsResponse{Chats: []archive.Chat{}}
	for _, ch := range all {
		if (req.Kind == "" || string(ch.Kind) == req.Kind) && c.canRead(ch.JID) {
			resp.Chats = append(resp.Chats, ch)
		}
	}
	s.Log.Info(yardlogs.EventWAAPIQuery, slog.String(yardlogs.KeyMessage, "api chats query"),
		slog.String(yardlogs.KeyEntityID, c.ID), slog.Int(yardlogs.KeyCount, len(resp.Chats)))
	apiJSON(w, http.StatusOK, resp)
}

// resolveReadable finds the requested chat and checks the client may read
// it. Clients limited to some chats get the same 403 for "not yours" and
// "does not exist", so they cannot probe the catalog.
func (s *APIServer) resolveReadable(w http.ResponseWriter, r *http.Request, c apiCaller, query string) (archive.Chat, bool) {
	chat, err := ResolveChat(r.Context(), s.Archive, query)
	everything := readsEverything(c.Scopes)
	var amb *archive.AmbiguousError
	switch {
	case err == nil:
		if c.canRead(chat.JID) {
			return chat, true
		}
	case errors.As(err, &amb):
		if everything {
			apiError(w, http.StatusBadRequest, "ambiguous", amb.Error())
			return chat, false
		}
		// A name shared with chats the client cannot see is not ambiguous
		// to it: pick the one it may read, if exactly one.
		var mine []archive.Chat
		for _, m := range amb.Matches {
			if c.canRead(m.JID) {
				mine = append(mine, m)
			}
		}
		if len(mine) == 1 {
			return mine[0], true
		}
		if len(mine) > 1 {
			apiError(w, http.StatusBadRequest, "ambiguous", (&archive.AmbiguousError{Query: query, Matches: mine}).Error())
			return chat, false
		}
	case errors.Is(err, archive.ErrNotFound):
		if everything {
			apiError(w, http.StatusNotFound, "unknown_chat", fmt.Sprintf("no chat matches %q", query))
			return chat, false
		}
	default:
		apiError(w, http.StatusInternalServerError, "internal", "archive read failed")
		return chat, false
	}
	s.denied(r, c.ID, "chat not in scopes: "+query)
	apiError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("not allowed to read %q: needs scope %s:<chat jid>", query, ReadScopeName))
	return chat, false
}

func (s *APIServer) denied(r *http.Request, client, why string) {
	s.Log.Warn(yardlogs.EventWAAPIDenied, slog.String(yardlogs.KeyMessage, why),
		slog.String(yardlogs.KeyEntityID, client), slog.String(yardlogs.KeyHTTPPath, r.URL.Path))
}

// archivedEvent renders an archived message as the same comms Message
// envelope that routes and events carry, so consumers parse one shape.
func archivedEvent(row archive.FeedRow, self string) Event {
	return messageEvent(row.ID, "", row.SenderJID, row.SenderName, self, row.ChatJID, row.ChatName, row.ChatKind,
		row.FromMe, row.Kind, row.Text, row.ReplyTo, row.Timestamp)
}

// messageEvent builds the comms Message v1 envelope for a WhatsApp message.
func messageEvent(id, traceID, sender, senderName, self, chatJID, chatName string, chatKind archive.ChatKind,
	fromMe bool, kind, text, replyTo string, ts time.Time) Event {
	return Event{
		SchemaVersion: 1,
		ID:            id,
		TraceID:       traceID,
		Channel:       EventChannel,
		Direction:     "inbound",
		From:          EventAddress{Channel: EventChannel, Raw: sender, Display: senderName},
		To:            []EventAddress{{Channel: EventChannel, Raw: self}},
		Body:          text,
		Metadata: map[string]string{
			"chat_jid":  chatJID,
			"chat_name": chatName,
			"chat_kind": string(chatKind),
			"from_me":   strconv.FormatBool(fromMe),
			"kind":      kind,
			"reply_to":  replyTo,
		},
		Timestamp: ts.UTC(),
	}
}

// Cursors are opaque to clients: "v1:<seq>" in URL-safe base64, so the
// encoding can change without breaking anyone who just echoes it back.
func encodeCursor(seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte("v1:" + strconv.FormatInt(seq, 10)))
}

func decodeCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		if n, ok := strings.CutPrefix(string(raw), "v1:"); ok {
			if seq, err := strconv.ParseInt(n, 10, 64); err == nil && seq >= 0 {
				return seq, nil
			}
		}
	}
	return 0, errors.New("invalid cursor: pass next_cursor from a previous response unchanged")
}

// parseSince accepts "" (no bound), a duration back from now, or RFC 3339.
func parseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	d, err := ParseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("since: want a duration (24h, 7d) or an RFC 3339 time, got %q", s)
	}
	return now.Add(-d), nil
}

func decodeBody(body []byte, v any) error {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("body: %w", err)
	}
	return nil
}

type apiErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func apiError(w http.ResponseWriter, status int, code, msg string) {
	var b apiErrorBody
	b.Error.Code, b.Error.Message = code, msg
	apiJSON(w, status, b)
}

func apiJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ServeAPI listens on addr (loopback only) until ctx ends.
func ServeAPI(ctx context.Context, addr string, h http.Handler) (stop func(), err error) {
	if err := ValidateAPIAddr(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("api: listen %s: %w", addr, err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	return func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		<-done
	}, nil
}

// ValidateAPIAddr requires "host:port" on a loopback address: the API
// trusts fairway's identity headers, so it must never face the network.
func ValidateAPIAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("api-addr %q: want host:port, e.g. 127.0.0.1:9877", addr)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("api-addr %q: port must be 1-65535", addr)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("api-addr %q: must be a loopback address (127.0.0.1, ::1 or localhost)", addr)
		}
	}
	return nil
}

// ReadAPIToken returns the API token, or "" when none was created.
func ReadAPIToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("api: read token: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// EnsureAPIToken returns the existing token or creates one (mode 0600).
// rotate replaces it unconditionally.
func EnsureAPIToken(path string, rotate bool) (string, error) {
	if !rotate {
		if t, err := ReadAPIToken(path); err != nil || t != "" {
			return t, err
		}
	}
	var b [apiTokenSize]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("api: token: %w", err)
	}
	t := "wat_" + hex.EncodeToString(b[:])
	if err := writeFileAtomic(path, []byte(t+"\n")); err != nil {
		return "", err
	}
	return t, nil
}
