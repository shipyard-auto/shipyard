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
	APISendPath     = "/v1/send"
	APILimitsPath   = "/v1/limits"
	APIMediaPath    = "/v1/media"
	APIUploadPath   = "/v1/upload"
)

// Query limits.
const (
	APIDefaultLimit = 100
	APIMaxLimit     = 500
	apiBatch        = 500
	apiMaxBody      = 64 << 10
	// apiMaxMediaBody admits one base64 chunk of APIChunkMax plus JSON;
	// fairway's own cap on forwarded bodies is 4 MB.
	apiMaxMediaBody = 4 << 20
	apiTokenSize    = 32
	// APIMediaChunk is the most file bytes one /v1/media response carries.
	// Base64 makes it ~2.7 MB of JSON, under fairway's 4 MB cap on
	// forwarded responses; larger files are read in several calls.
	APIMediaChunk = 2 << 20
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
	// Uploads holds chunked uploads for /v1/upload; nil disables them.
	Uploads *UploadStore
	// Outbox sends for /v1/send (every policy check applies); /v1/limits
	// reads its config. Nil disables both.
	Outbox *Outbox
}

// Handler returns the HTTP handler.
func (s *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(APIMessagesPath, s.guard(s.messages))
	mux.HandleFunc(APIChatsPath, s.guard(s.chats))
	mux.HandleFunc(APISendPath, s.guardBody(s.send, apiMaxMediaBody))
	mux.HandleFunc(APIUploadPath, s.guardBody(s.upload, apiMaxMediaBody))
	mux.HandleFunc(APILimitsPath, s.guard(s.limits))
	mux.HandleFunc(APIMediaPath, s.guard(s.media))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		apiError(w, http.StatusNotFound, "not_found", "unknown endpoint; use POST "+
			strings.Join([]string{APIMessagesPath, APIChatsPath, APISendPath, APILimitsPath, APIMediaPath, APIUploadPath}, ", "))
	})
	return mux
}

// apiCaller is the client fairway authenticated.
type apiCaller struct {
	ID     string
	Scopes []string
}

func (c apiCaller) can(scopeName, chatJID string) bool {
	return grants(c.Scopes, scopeName+":"+chatJID)
}

func (c apiCaller) canRead(chatJID string) bool { return c.can(ReadScopeName, chatJID) }

type apiHandler func(w http.ResponseWriter, r *http.Request, c apiCaller, body []byte)

// guard checks method, the fairway token and the forwarded identity.
func (s *APIServer) guard(next apiHandler) http.HandlerFunc { return s.guardBody(next, apiMaxBody) }

// guardBody is guard with a body size limit of its own.
func (s *APIServer) guardBody(next apiHandler, maxBody int) http.HandlerFunc {
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
		body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBody)+1))
		if err != nil || len(body) > maxBody {
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
		chat, ok := s.resolveFor(w, r, c, req.Chat, ReadScopeName)
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

// resolveFor finds the requested chat and checks the client holds
// scopeName on it. Clients limited to some chats get the same 403 for "not
// yours" and "does not exist", so they cannot probe the catalog.
func (s *APIServer) resolveFor(w http.ResponseWriter, r *http.Request, c apiCaller, query, scopeName string) (archive.Chat, bool) {
	chat, err := ResolveChat(r.Context(), s.Archive, query)
	everything := coversAll(c.Scopes, scopeName)
	var amb *archive.AmbiguousError
	switch {
	case err == nil:
		if c.can(scopeName, chat.JID) {
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
			if c.can(scopeName, m.JID) {
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
	verb := "read"
	if scopeName == SendScopeName {
		verb = "send to"
	}
	apiError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("not allowed to %s %q: needs scope %s:<chat jid>", verb, query, scopeName))
	return chat, false
}

// SendAPIRequest is the body of POST /v1/send.
type SendAPIRequest struct {
	Chat string `json:"chat"`
	Text string `json:"text"`
	// IdempotencyKey makes retries safe; strongly recommended. Keys are
	// per client: two clients may use the same key independently.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	DryRun         bool   `json:"dry_run,omitempty"`
	// Media attaches a file: inline (data, up to 2 MiB) or a finished
	// upload (upload_id, see /v1/upload). Text becomes the caption.
	Media *SendAPIMedia `json:"media,omitempty"`
}

// SendAPIMedia is the attachment of an API send. Exactly one of Data and
// UploadID.
type SendAPIMedia struct {
	Data      string `json:"data,omitempty"` // base64
	UploadID  string `json:"upload_id,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	FileName  string `json:"file_name,omitempty"`
	VoiceNote bool   `json:"voice_note,omitempty"`
}

// UploadAPIRequest is the body of POST /v1/upload: one chunk.
type UploadAPIRequest struct {
	UploadID string `json:"upload_id,omitempty"` // empty on the first chunk
	Offset   int64  `json:"offset"`
	Data     string `json:"data"` // base64, up to 2 MiB decoded
}

func (s *APIServer) upload(w http.ResponseWriter, r *http.Request, c apiCaller, body []byte) {
	if s.Outbox == nil || s.Uploads == nil {
		apiError(w, http.StatusServiceUnavailable, "unavailable", "sending is not available on this daemon")
		return
	}
	if !grants(c.Scopes, SendScopeName) {
		s.denied(r, c.ID, "upload without "+SendScopeName)
		apiError(w, http.StatusForbidden, "forbidden", "uploading needs scope "+SendScopeName)
		return
	}
	var req UploadAPIRequest
	if err := decodeBody(body, &req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	switch {
	case err != nil:
		apiError(w, http.StatusBadRequest, "invalid", "data is not standard base64")
		return
	case len(data) == 0 || len(data) > APIChunkMax:
		apiError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("a chunk carries 1 byte to %s", FormatBytes(APIChunkMax)))
		return
	}
	cfg, err := s.Outbox.Config()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal", "config unreadable")
		return
	}
	info, err := s.Uploads.Append(c.ID, req.UploadID, req.Offset, data, cfg.Media.SendMaxBytes)
	switch {
	case err == nil:
		apiJSON(w, http.StatusOK, info)
	case errors.Is(err, ErrUploadUnknown):
		apiError(w, http.StatusNotFound, "unknown_upload", err.Error())
	case errors.Is(err, ErrUploadOffset):
		apiError(w, http.StatusConflict, "offset_mismatch",
			fmt.Sprintf("%v: the upload has %d bytes; send offset %d", err, info.Size, info.Size))
	case errors.Is(err, ErrUploadTooLarge):
		apiError(w, http.StatusRequestEntityTooLarge, "too_long",
			fmt.Sprintf("%v (%s)", err, FormatBytes(cfg.Media.SendMaxBytes)))
	case errors.Is(err, ErrUploadTooMany):
		apiError(w, http.StatusTooManyRequests, "too_many_uploads",
			fmt.Sprintf("%v: at most %d per client; unused ones expire after %s", err, MaxUploadsPerClient, UploadTTL))
	default:
		apiError(w, http.StatusInternalServerError, "internal", "upload failed")
	}
}

// outMedia turns an API attachment into the Outbox's.
func (s *APIServer) outMedia(c apiCaller, m *SendAPIMedia) (*OutMedia, string, error) {
	out := &OutMedia{MimeType: m.MimeType, FileName: m.FileName, VoiceNote: m.VoiceNote}
	switch {
	case (m.Data == "") == (m.UploadID == ""):
		return nil, "invalid", errors.New("media needs exactly one of data and upload_id")
	case m.Data != "":
		data, err := base64.StdEncoding.DecodeString(m.Data)
		if err != nil {
			return nil, "invalid", errors.New("media.data is not standard base64")
		}
		if len(data) > APIChunkMax {
			return nil, "too_long", fmt.Errorf("inline media is limited to %s; send larger files through %s and pass upload_id",
				FormatBytes(APIChunkMax), APIUploadPath)
		}
		out.Data = data
	default:
		if s.Uploads == nil {
			return nil, "unknown_upload", ErrUploadUnknown
		}
		p, err := s.Uploads.Path(c.ID, m.UploadID)
		if err != nil {
			return nil, "unknown_upload", err
		}
		out.Path = p
	}
	return out, "", nil
}

// sendStatus maps refusal codes to HTTP statuses; the body always carries
// the code itself, which is what clients should branch on.
var sendStatus = map[SendCode]int{
	CodeInvalid:        http.StatusBadRequest,
	CodeTooLong:        http.StatusBadRequest,
	CodeUnknownChat:    http.StatusNotFound,
	CodeGroupsDisabled: http.StatusForbidden,
	CodeNotAllowed:     http.StatusForbidden,
	CodeNoHistory:      http.StatusConflict,
	CodeRateLimited:    http.StatusTooManyRequests,
	CodePaused:         http.StatusServiceUnavailable,
	CodeNotConnected:   http.StatusServiceUnavailable,
	CodeSendFailed:     http.StatusBadGateway,
}

func (s *APIServer) send(w http.ResponseWriter, r *http.Request, c apiCaller, body []byte) {
	if s.Outbox == nil {
		apiError(w, http.StatusServiceUnavailable, "unavailable", "sending is not available on this daemon")
		return
	}
	var req SendAPIRequest
	if err := decodeBody(body, &req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if strings.TrimSpace(req.Chat) == "" {
		apiError(w, http.StatusBadRequest, "invalid", "chat is required")
		return
	}
	chat, ok := s.resolveFor(w, r, c, req.Chat, SendScopeName)
	if !ok {
		return
	}
	key := ""
	if req.IdempotencyKey != "" {
		key = c.ID + "/" + req.IdempotencyKey
	}
	sr := SendRequest{Chat: chat.JID, Text: req.Text, IdempotencyKey: key, DryRun: req.DryRun, Client: c.ID}
	if req.Media != nil {
		m, code, err := s.outMedia(c, req.Media)
		if err != nil {
			status := map[string]int{"unknown_upload": http.StatusNotFound, "too_long": http.StatusRequestEntityTooLarge}[code]
			if status == 0 {
				status = http.StatusBadRequest
			}
			apiError(w, status, code, err.Error())
			return
		}
		sr.Media = m
	}
	res, err := s.Outbox.Send(r.Context(), sr)
	if err != nil {
		var se *SendError
		if !errors.As(err, &se) {
			apiError(w, http.StatusInternalServerError, "internal", "send failed")
			return
		}
		status, ok := sendStatus[se.Code]
		if !ok {
			status = http.StatusInternalServerError
		}
		var b apiErrorBody
		b.Error.Code, b.Error.Message = string(se.Code), se.Msg
		if se.RetryAfter > 0 {
			secs := int(se.RetryAfter / time.Second)
			b.Error.RetryAfterSeconds = secs
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
		apiJSON(w, status, b)
		return
	}
	apiJSON(w, http.StatusOK, res)
}

// LimitsAPIRequest is the body of POST /v1/limits.
type LimitsAPIRequest struct {
	// Chat, when set, adds whether the client can send there right now.
	Chat string `json:"chat,omitempty"`
}

func (s *APIServer) limits(w http.ResponseWriter, r *http.Request, c apiCaller, body []byte) {
	if s.Outbox == nil {
		apiError(w, http.StatusServiceUnavailable, "unavailable", "sending is not available on this daemon")
		return
	}
	var req LimitsAPIRequest
	if err := decodeBody(body, &req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	var chat *archive.Chat
	if req.Chat != "" {
		ch, ok := s.resolveFor(w, r, c, req.Chat, SendScopeName)
		if !ok {
			return
		}
		chat = &ch
	}
	cfg, err := s.Outbox.Config()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal", "config unreadable")
		return
	}
	rep, err := BuildLimitsReport(r.Context(), s.Archive, cfg, s.Outbox.Now(), chat)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal", "archive read failed")
		return
	}
	apiJSON(w, http.StatusOK, rep)
}

// MediaRequest is the body of POST /v1/media.
type MediaRequest struct {
	Chat string `json:"chat"` // JID, name or phone
	ID   string `json:"id"`   // message id (the envelope's "id")
	// Offset and Length select a byte range; Length 0 (or above
	// APIMediaChunk) means "as much as one response carries".
	Offset int64 `json:"offset,omitempty"`
	Length int64 `json:"length,omitempty"`
}

// MediaResponse is one chunk of an attachment. Keep calling with
// offset = offset + length until eof.
type MediaResponse struct {
	Chat     string `json:"chat"`
	ID       string `json:"id"`
	MimeType string `json:"mime_type,omitempty"`
	FileName string `json:"file_name,omitempty"`
	Size     int64  `json:"size"`   // whole file, bytes
	SHA256   string `json:"sha256"` // whole file, hex
	Offset   int64  `json:"offset"`
	Length   int64  `json:"length"` // bytes in data
	EOF      bool   `json:"eof"`
	Data     []byte `json:"data"` // base64
}

func (s *APIServer) media(w http.ResponseWriter, r *http.Request, c apiCaller, body []byte) {
	var req MediaRequest
	if err := decodeBody(body, &req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	switch {
	case strings.TrimSpace(req.Chat) == "" || strings.TrimSpace(req.ID) == "":
		apiError(w, http.StatusBadRequest, "invalid", "chat and id are required")
		return
	case req.Offset < 0 || req.Length < 0:
		apiError(w, http.StatusBadRequest, "invalid", "offset and length must not be negative")
		return
	}
	chat, ok := s.resolveFor(w, r, c, req.Chat, ReadScopeName)
	if !ok {
		return
	}
	m, err := s.Archive.GetMedia(r.Context(), chat.JID, req.ID)
	switch {
	case errors.Is(err, archive.ErrNoMedia):
		apiError(w, http.StatusNotFound, "no_media", "no archived attachment for this message (only watched chats keep media)")
		return
	case err != nil:
		apiError(w, http.StatusInternalServerError, "internal", "archive read failed")
		return
	case m.Status != archive.MediaStored:
		msg := "attachment is " + string(m.Status)
		if m.Reason != "" {
			msg += ": " + m.Reason
		}
		status := http.StatusNotFound
		if m.Status == archive.MediaPending {
			status = http.StatusConflict // try again shortly
		}
		apiError(w, status, "media_"+string(m.Status), msg)
		return
	}
	f, err := os.Open(m.Path)
	if err != nil {
		apiError(w, http.StatusNotFound, "media_missing", "the attachment file is gone (expired or deleted)")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal", "media read failed")
		return
	}
	size := info.Size()
	if req.Offset > size {
		apiError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("offset %d is past the end (size %d)", req.Offset, size))
		return
	}
	n := req.Length
	if n == 0 || n > APIMediaChunk {
		n = APIMediaChunk
	}
	n = min(n, size-req.Offset)
	data := make([]byte, n)
	if _, err := f.ReadAt(data, req.Offset); err != nil && !errors.Is(err, io.EOF) {
		apiError(w, http.StatusInternalServerError, "internal", "media read failed")
		return
	}
	s.Log.Info(yardlogs.EventWAAPIQuery, slog.String(yardlogs.KeyMessage, "api media read"),
		slog.String(yardlogs.KeyEntityID, c.ID), slog.String(yardlogs.KeyChatJID, chat.JID),
		slog.Int64(yardlogs.KeySizeBytes, n))
	apiJSON(w, http.StatusOK, MediaResponse{
		Chat: chat.JID, ID: req.ID, MimeType: m.MimeType, FileName: m.FileName, Size: size, SHA256: m.SHA256,
		Offset: req.Offset, Length: n, EOF: req.Offset+n >= size, Data: data,
	})
}

func (s *APIServer) denied(r *http.Request, client, why string) {
	s.Log.Warn(yardlogs.EventWAAPIDenied, slog.String(yardlogs.KeyMessage, why),
		slog.String(yardlogs.KeyEntityID, client), slog.String(yardlogs.KeyHTTPPath, r.URL.Path))
}

// archivedEvent renders an archived message as the same comms Message
// envelope that routes and events carry, so consumers parse one shape.
func archivedEvent(row archive.FeedRow, self string) Event {
	return messageEvent(row.ID, "", row.SenderJID, row.SenderName, self, row.ChatJID, row.ChatName, row.ChatKind,
		row.FromMe, row.Kind, row.Text, row.ReplyTo, row.Timestamp, row.Media)
}

// messageEvent builds the comms Message v1 envelope for a WhatsApp message.
//
// Attachment metadata rides in media_* keys; the file's local path does
// not (see withMediaPath): this envelope also leaves the machine.
func messageEvent(id, traceID, sender, senderName, self, chatJID, chatName string, chatKind archive.ChatKind,
	fromMe bool, kind, text, replyTo string, ts time.Time, media *archive.Media) Event {
	ev := Event{
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
	if media != nil {
		md := ev.Metadata
		md["media_status"] = string(media.Status)
		set := func(k, v string) {
			if v != "" && v != "0" && v != "false" {
				md[k] = v
			}
		}
		set("media_reason", media.Reason)
		set("media_mime_type", media.MimeType)
		set("media_size", strconv.FormatInt(media.Size, 10))
		set("media_seconds", strconv.Itoa(media.Seconds))
		set("media_voice_note", strconv.FormatBool(media.VoiceNote))
		set("media_file_name", media.FileName)
		set("media_sha256", media.SHA256)
	}
	return ev
}

// withMediaPath adds the stored file's local path for route deliveries,
// which reach agents on this machine. Published events and the query API
// leave it out: their consumers fetch the bytes through /v1/media.
func withMediaPath(ev Event, media *archive.Media) Event {
	if media == nil || media.Path == "" {
		return ev
	}
	md := make(map[string]string, len(ev.Metadata)+1)
	for k, v := range ev.Metadata {
		md[k] = v
	}
	md["media_path"] = media.Path
	ev.Metadata = md
	return ev
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
		// RetryAfterSeconds, for rate_limited, is when to try again (also
		// sent as the Retry-After header).
		RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
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
