package wa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Link previews: WhatsApp has no server that renders them. The sending
// client fetches the page and embeds title, description and image in the
// message; recipients show what they get. The daemon never fetches the
// URL itself: the caller supplies the preview, so a send cannot make this
// machine request an arbitrary host.
//
// The image goes twice: a small JPEG inline (what older clients show, and
// the placeholder while loading) and the full image uploaded to the CDN as
// a link thumbnail, which current clients render as the large card above
// the text.

// OutLinkPreview is the preview card of a text send.
type OutLinkPreview struct {
	// URL is the link the card opens. It must appear verbatim in the text.
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Image is optional; JPEG or PNG.
	Image *OutMedia `json:"image,omitempty"`
}

// Preview limits.
const (
	MaxPreviewTitle       = 300
	MaxPreviewDescription = 1000
	// linkThumbMaxSide is the longest side of the inline thumbnail.
	linkThumbMaxSide = 192
	// linkThumbKind is the upload kind of a preview image.
	linkThumbKind = "link_thumbnail"
)

// LinkPreview is a validated preview, ready to send.
type LinkPreview struct {
	URL         string
	Title       string
	Description string
	// Image is the full JPEG (PNGs are re-encoded); nil without an image.
	Image     []byte
	Width     int
	Height    int
	Thumbnail []byte // small JPEG embedded in the message
	SHA256    string // hex of Image
}

// preparePreview validates a preview against the text it decorates.
func preparePreview(p *OutLinkPreview, text string, maxBytes int64) (*LinkPreview, *SendError) {
	u, err := url.Parse(p.URL)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return nil, refuse(CodeInvalid, "link preview url %q is not an http(s) URL", p.URL)
	case !strings.Contains(text, p.URL):
		return nil, refuse(CodeInvalid, "link preview url %q does not appear in the text", p.URL)
	case !utf8.ValidString(p.Title) || !utf8.ValidString(p.Description):
		return nil, refuse(CodeInvalid, "link preview title and description must be valid UTF-8")
	case utf8.RuneCountInString(p.Title) > MaxPreviewTitle:
		return nil, refuse(CodeTooLong, "link preview title has %d characters; the limit is %d", utf8.RuneCountInString(p.Title), MaxPreviewTitle)
	case utf8.RuneCountInString(p.Description) > MaxPreviewDescription:
		return nil, refuse(CodeTooLong, "link preview description has %d characters; the limit is %d",
			utf8.RuneCountInString(p.Description), MaxPreviewDescription)
	}
	lp := &LinkPreview{URL: p.URL, Title: strings.TrimSpace(p.Title), Description: strings.TrimSpace(p.Description)}
	if p.Image == nil {
		return lp, nil
	}
	m, se := prepareMedia(p.Image, maxBytes)
	if se != nil {
		return nil, se
	}
	if m.Kind != SendKindImage {
		return nil, refuse(CodeInvalid, "link preview image must be JPEG or PNG (got %s)", m.MimeType)
	}
	// A small, well-compressed PNG can still decode to gigabytes.
	if m.Width*m.Height > maxThumbPixels {
		return nil, refuse(CodeTooLong, "link preview image is %dx%d; the limit is %d pixels", m.Width, m.Height, maxThumbPixels)
	}
	src, _, err := image.Decode(bytes.NewReader(m.Data))
	if err != nil {
		return nil, refuse(CodeInvalid, "link preview image does not decode")
	}
	lp.Image = m.Data
	if m.MimeType != "image/jpeg" {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
			return nil, refuse(CodeInvalid, "link preview image could not be converted to JPEG")
		}
		lp.Image = buf.Bytes()
	}
	lp.Width, lp.Height = src.Bounds().Dx(), src.Bounds().Dy()
	lp.Thumbnail = scaleJPEG(src, linkThumbMaxSide, 75)
	sum := sha256.Sum256(lp.Image)
	lp.SHA256 = hex.EncodeToString(sum[:])
	return lp, nil
}

// sendLink sends text with a preview card. The large image is uploaded
// (or reused from a recent upload of the same bytes); if that fails the
// card still goes out with the inline thumbnail, which beats no message.
func (o *Outbox) sendLink(ctx context.Context, jid types.JID, text string, p *LinkPreview) (string, time.Time, error) {
	var up *UploadedMedia
	if p.Image != nil {
		key := linkThumbKind + ":" + p.SHA256
		cached, ok := o.uploads.get(key, o.Now())
		if !ok {
			var err error
			if cached, err = o.Sender.Upload(ctx, p.Image, linkThumbKind); err == nil {
				cached.UploadedAt = o.Now()
				o.uploads.put(key, cached, cached.UploadedAt)
				ok = true
			} else {
				o.logPreviewUploadError(jid.String(), err)
			}
		}
		if ok {
			up = &cached
		}
	}
	return o.Sender.SendLink(ctx, jid, text, p, up)
}

func (c clientSender) SendLink(ctx context.Context, chat types.JID, text string, p *LinkPreview, up *UploadedMedia) (string, time.Time, error) {
	resp, err := c.cli.SendMessage(ctx, chat, buildLinkMessage(text, p, up))
	if err != nil {
		return "", time.Time{}, err
	}
	return resp.ID, resp.Timestamp, nil
}

// buildLinkMessage renders the protobuf of a text with a preview card.
// up is the uploaded large image, nil when there is none.
func buildLinkMessage(text string, p *LinkPreview, up *UploadedMedia) *waE2E.Message {
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return proto.String(s)
	}
	et := &waE2E.ExtendedTextMessage{
		Text:          proto.String(text),
		MatchedText:   proto.String(p.URL),
		Title:         opt(p.Title),
		Description:   opt(p.Description),
		PreviewType:   waE2E.ExtendedTextMessage_NONE.Enum(),
		JPEGThumbnail: p.Thumbnail,
	}
	if up != nil {
		et.ThumbnailDirectPath = proto.String(up.DirectPath)
		et.ThumbnailSHA256 = up.FileSHA256
		et.ThumbnailEncSHA256 = up.FileEncSHA256
		et.MediaKey = up.MediaKey
		// The key's age, not the send's: a cached upload can be 30 min old.
		et.MediaKeyTimestamp = proto.Int64(up.UploadedAt.Unix())
		et.ThumbnailWidth = proto.Uint32(uint32(p.Width))
		et.ThumbnailHeight = proto.Uint32(uint32(p.Height))
	}
	return &waE2E.Message{ExtendedTextMessage: et}
}
