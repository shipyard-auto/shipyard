package wa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // decode PNGs for dimensions and thumbnails
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Sending media: the attachment is read and classified here, uploaded
// (encrypted) to WhatsApp's CDN once, and referenced by the message. The
// same policy as text applies — Outbox.send runs every check first.

// OutMedia is the attachment of a send. Exactly one of Path and Data.
type OutMedia struct {
	// Path is a local file the daemon reads. Socket callers (the CLI, crew
	// tools) run as the same user, so it grants nothing they lack; the API
	// never accepts one.
	Path string `json:"path,omitempty"`
	// Data is the file itself, set by the API.
	Data []byte `json:"-"`
	// MimeType is optional: derived from the file name, then the bytes.
	MimeType string `json:"mime_type,omitempty"`
	// FileName names a document for the recipient (default: Path's name).
	FileName string `json:"file_name,omitempty"`
	// VoiceNote sends an Ogg/Opus audio as a push-to-talk voice note.
	VoiceNote bool `json:"voice_note,omitempty"`
}

// Media kinds the daemon sends.
const (
	SendKindText  = "text"
	SendKindImage = "image"
	SendKindVideo = "video"
	SendKindAudio = "audio"
	SendKindFile  = "file"
)

// MediaMessage is a prepared attachment, ready to upload and send.
type MediaMessage struct {
	Kind      string
	Data      []byte
	MimeType  string
	FileName  string
	Caption   string
	VoiceNote bool
	Width     int
	Height    int
	Thumbnail []byte // JPEG, images only
	SHA256    string // hex of Data
}

// UploadedMedia is an attachment on WhatsApp's CDN.
type UploadedMedia struct {
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileEncSHA256 []byte
	FileSHA256    []byte
	FileLength    uint64
	// UploadedAt is when the Outbox uploaded it (link previews carry it).
	UploadedAt time.Time
}

// thumbMaxSide is the longest side of image thumbnails, in pixels; about
// what WhatsApp itself embeds for the blurred preview.
const thumbMaxSide = 72

// maxThumbPixels skips thumbnails for images too large to decode cheaply.
const maxThumbPixels = 40_000_000

// prepareMedia reads, classifies and validates an attachment.
func prepareMedia(m *OutMedia, maxBytes int64) (*MediaMessage, *SendError) {
	data, name, err := readMedia(m, maxBytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	mm := &MediaMessage{Data: data, FileName: name, VoiceNote: m.VoiceNote, SHA256: hex.EncodeToString(sum[:])}

	sniffed := baseMime(http.DetectContentType(data[:min(len(data), 512)]))
	declared := baseMime(m.MimeType)
	if declared == "" {
		declared = baseMime(mime.TypeByExtension(strings.ToLower(filepath.Ext(name))))
	}
	if declared == "" {
		declared = sniffed
	}

	switch {
	case declared == "image/jpeg" || declared == "image/png":
		if sniffed != "image/jpeg" && sniffed != "image/png" {
			return nil, refuse(CodeInvalid, "the file is not a JPEG or PNG image (its content looks like %s)", sniffed)
		}
		mm.Kind, mm.MimeType = SendKindImage, sniffed
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			mm.Width, mm.Height = cfg.Width, cfg.Height
			if cfg.Width*cfg.Height <= maxThumbPixels {
				mm.Thumbnail = thumbnail(data)
			}
		}
	case declared == "video/mp4":
		if sniffed != "video/mp4" {
			return nil, refuse(CodeInvalid, "the file is not an MP4 video (its content looks like %s)", sniffed)
		}
		mm.Kind, mm.MimeType = SendKindVideo, "video/mp4"
	case strings.HasPrefix(declared, "audio/") || sniffed == "application/ogg":
		mm.Kind, mm.MimeType = SendKindAudio, declared
		if sniffed == "application/ogg" || declared == "audio/ogg" {
			mm.MimeType = "audio/ogg; codecs=opus"
		}
	default:
		mm.Kind, mm.MimeType = SendKindFile, declared
		if mm.MimeType == "" {
			mm.MimeType = "application/octet-stream"
		}
	}
	if m.VoiceNote && !(mm.Kind == SendKindAudio && strings.HasPrefix(mm.MimeType, "audio/ogg")) {
		return nil, refuse(CodeInvalid, "a voice note must be Ogg/Opus audio (got %s)", mm.MimeType)
	}
	return mm, nil
}

func readMedia(m *OutMedia, maxBytes int64) ([]byte, string, *SendError) {
	tooBig := func(n int64) *SendError {
		return refuse(CodeTooLong, "the file has %s; the limit is %s (config media-send-max-size)", FormatBytes(n), FormatBytes(maxBytes))
	}
	switch {
	case m.Path != "" && m.Data != nil:
		return nil, "", refuse(CodeInvalid, "pass a file path or data, not both")
	case m.Path != "":
		if !filepath.IsAbs(m.Path) {
			return nil, "", refuse(CodeInvalid, "file path %q must be absolute", m.Path)
		}
		info, err := os.Stat(m.Path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, "", refuse(CodeInvalid, "cannot read file %q", m.Path)
		}
		if info.Size() > maxBytes {
			return nil, "", tooBig(info.Size())
		}
		data, err := os.ReadFile(m.Path)
		if err != nil {
			return nil, "", refuse(CodeInvalid, "cannot read file %q", m.Path)
		}
		name := m.FileName
		if name == "" {
			name = filepath.Base(m.Path)
		}
		if len(data) == 0 {
			return nil, "", refuse(CodeInvalid, "file %q is empty", m.Path)
		}
		return data, name, nil
	case len(m.Data) > 0:
		if int64(len(m.Data)) > maxBytes {
			return nil, "", tooBig(int64(len(m.Data)))
		}
		return m.Data, m.FileName, nil
	}
	return nil, "", refuse(CodeInvalid, "the attachment is empty")
}

func baseMime(t string) string {
	b, _, _ := strings.Cut(t, ";")
	return strings.ToLower(strings.TrimSpace(b))
}

// thumbnail renders a small JPEG preview (nearest-neighbour; it is only
// shown blurred while the full image loads). nil when the image does not
// decode.
func thumbnail(data []byte) []byte {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return scaleJPEG(src, thumbMaxSide, 60)
}

// scaleJPEG shrinks src to fit side×side (nearest-neighbour) and encodes
// it as JPEG; nil for an empty image.
func scaleJPEG(src image.Image, side, quality int) []byte {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil
	}
	tw, th := min(w, side), min(h, side)
	if w >= h {
		th = max(1, h*tw/w)
	} else {
		tw = max(1, w*th/h)
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := range th {
		for x := range tw {
			dst.Set(x, y, src.At(b.Min.X+x*w/tw, b.Min.Y+y*h/th))
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// uploadCacheTTL is how long an uploaded attachment is reused: a broadcast
// of one image to several groups uploads it once.
const uploadCacheTTL = 30 * time.Minute

type uploadCache struct {
	mu    sync.Mutex
	items map[string]cachedUpload
}

type cachedUpload struct {
	up UploadedMedia
	at time.Time
}

func (c *uploadCache) get(key string, now time.Time) (UploadedMedia, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok || now.Sub(e.at) > uploadCacheTTL {
		delete(c.items, key)
		return UploadedMedia{}, false
	}
	return e.up, true
}

func (c *uploadCache) put(key string, up UploadedMedia, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]cachedUpload{}
	}
	for k, e := range c.items {
		if now.Sub(e.at) > uploadCacheTTL {
			delete(c.items, k)
		}
	}
	c.items[key] = cachedUpload{up: up, at: now}
}

// mediaType maps a send kind to whatsmeow's upload key type.
func mediaType(kind string) whatsmeow.MediaType {
	switch kind {
	case SendKindImage:
		return whatsmeow.MediaImage
	case SendKindVideo:
		return whatsmeow.MediaVideo
	case SendKindAudio:
		return whatsmeow.MediaAudio
	case linkThumbKind:
		return whatsmeow.MediaLinkThumbnail
	default:
		return whatsmeow.MediaDocument
	}
}

func (c clientSender) Upload(ctx context.Context, data []byte, kind string) (UploadedMedia, error) {
	r, err := c.cli.Upload(ctx, data, mediaType(kind))
	if err != nil {
		return UploadedMedia{}, err
	}
	return UploadedMedia{URL: r.URL, DirectPath: r.DirectPath, MediaKey: r.MediaKey,
		FileEncSHA256: r.FileEncSHA256, FileSHA256: r.FileSHA256, FileLength: r.FileLength}, nil
}

func (c clientSender) SendMedia(ctx context.Context, chat types.JID, up UploadedMedia, m *MediaMessage) (string, time.Time, error) {
	msg := buildMediaMessage(up, m)
	if msg == nil {
		return "", time.Time{}, fmt.Errorf("unknown media kind %q", m.Kind)
	}
	resp, err := c.cli.SendMessage(ctx, chat, msg)
	if err != nil {
		return "", time.Time{}, err
	}
	return resp.ID, resp.Timestamp, nil
}

// buildMediaMessage renders the protobuf for an uploaded attachment.
func buildMediaMessage(up UploadedMedia, m *MediaMessage) *waE2E.Message {
	caption := func() *string {
		if m.Caption == "" {
			return nil
		}
		return proto.String(m.Caption)
	}
	switch m.Kind {
	case SendKindImage:
		im := &waE2E.ImageMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Mimetype: proto.String(m.MimeType), Caption: caption(), JPEGThumbnail: m.Thumbnail,
		}
		if m.Width > 0 && m.Height > 0 {
			im.Width, im.Height = proto.Uint32(uint32(m.Width)), proto.Uint32(uint32(m.Height))
		}
		return &waE2E.Message{ImageMessage: im}
	case SendKindVideo:
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Mimetype: proto.String(m.MimeType), Caption: caption(),
		}}
	case SendKindAudio:
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Mimetype: proto.String(m.MimeType), PTT: proto.Bool(m.VoiceNote),
		}}
	case SendKindFile:
		name := m.FileName
		if name == "" {
			name = "file" + mediaExt(m.MimeType, "")
		}
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Mimetype: proto.String(m.MimeType), FileName: proto.String(name), Title: proto.String(name), Caption: caption(),
		}}
	}
	return nil
}
