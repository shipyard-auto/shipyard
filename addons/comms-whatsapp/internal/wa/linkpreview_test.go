package wa

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"
)

const promoLink = "https://amzn.to/4k540Pl"

func (h *outboxHarness) sendLink(text string, p *OutLinkPreview) (SendResult, error) {
	return h.ob.Send(context.Background(), SendRequest{Chat: viniJID.String(), Text: text, LinkPreview: p})
}

func TestSendLinkPreviewUploadsImageOnce(t *testing.T) {
	h := newOutbox(t)
	text := "🔥 Air fryer 40% off\n👉 " + promoLink
	p := &OutLinkPreview{URL: promoLink, Title: "Air fryer Mondial", Description: "amazon.com.br", Image: &OutMedia{Data: testJPEG(t, 1000, 800)}}
	res, err := h.sendLink(text, p)
	if err != nil || res.Kind != SendKindText || res.MsgID != "MSG1" {
		t.Fatalf("send → %+v %v", res, err)
	}
	if len(h.sender.links) != 1 || h.sender.uploads != 1 {
		t.Fatalf("links=%d uploads=%d calls=%v", len(h.sender.links), h.sender.uploads, h.sender.calls)
	}
	got := h.sender.links[0]
	if got.text != text || got.up == nil || got.preview.Title != "Air fryer Mondial" || got.preview.Width != 1000 || got.preview.Height != 800 {
		t.Fatalf("sent %+v", got)
	}
	if !strings.HasPrefix(h.sender.calls[len(h.sender.calls)-2], "upload:"+linkThumbKind+":") {
		t.Fatalf("calls=%v", h.sender.calls)
	}
	thumb, err := jpeg.DecodeConfig(bytes.NewReader(got.preview.Thumbnail))
	if err != nil || thumb.Width != linkThumbMaxSide || thumb.Height != 153 {
		t.Fatalf("thumbnail %+v %v", thumb, err)
	}
	// The same image to another chat (or again) reuses the upload.
	if _, err := h.sendLink(text+" ", p); err != nil {
		t.Fatal(err)
	}
	if h.sender.uploads != 1 || h.sender.links[1].up == nil {
		t.Fatalf("uploads=%d", h.sender.uploads)
	}
}

func TestSendLinkPreviewWithoutImage(t *testing.T) {
	h := newOutbox(t)
	if _, err := h.sendLink("olha "+promoLink, &OutLinkPreview{URL: promoLink, Title: "Oferta"}); err != nil {
		t.Fatal(err)
	}
	got := h.sender.links[0]
	if h.sender.uploads != 0 || got.up != nil || got.preview.Thumbnail != nil {
		t.Fatalf("uploads=%d sent=%+v", h.sender.uploads, got)
	}
}

func TestSendLinkPreviewConvertsPNG(t *testing.T) {
	h := newOutbox(t)
	if _, err := h.sendLink(promoLink, &OutLinkPreview{URL: promoLink, Image: &OutMedia{Data: testPNG(t)}}); err != nil {
		t.Fatal(err)
	}
	p := h.sender.links[0].preview
	if _, format, err := image.DecodeConfig(bytes.NewReader(p.Image)); err != nil || format != "jpeg" || p.Width != 40 || p.Height != 20 {
		t.Fatalf("image format=%s %dx%d err=%v", format, p.Width, p.Height, err)
	}
}

func TestSendLinkPreviewFailedUploadKeepsSmallThumbnail(t *testing.T) {
	h := newOutbox(t)
	h.sender.upFail = errors.New("cdn down")
	if _, err := h.sendLink(promoLink, &OutLinkPreview{URL: promoLink, Image: &OutMedia{Data: testJPEG(t, 300, 300)}}); err != nil {
		t.Fatal(err)
	}
	got := h.sender.links[0]
	if got.up != nil || len(got.preview.Thumbnail) == 0 {
		t.Fatalf("sent %+v", got)
	}
}

func TestSendLinkPreviewRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		text  string
		p     OutLinkPreview
		media *OutMedia
		code  SendCode
	}{
		"url not in text":   {"sem link", OutLinkPreview{URL: promoLink}, nil, CodeInvalid},
		"not http":          {"ftp://x/y", OutLinkPreview{URL: "ftp://x/y"}, nil, CodeInvalid},
		"no host":           {"https:///y", OutLinkPreview{URL: "https:///y"}, nil, CodeInvalid},
		"title too long":    {promoLink, OutLinkPreview{URL: promoLink, Title: strings.Repeat("a", MaxPreviewTitle+1)}, nil, CodeTooLong},
		"image not image":   {promoLink, OutLinkPreview{URL: promoLink, Image: &OutMedia{Data: testPDF}}, nil, CodeInvalid},
		"with media":        {promoLink, OutLinkPreview{URL: promoLink}, &OutMedia{Data: testJPEG(t, 10, 10)}, CodeInvalid},
		"description long":  {promoLink, OutLinkPreview{URL: promoLink, Description: strings.Repeat("é", MaxPreviewDescription+1)}, nil, CodeTooLong},
		"image is empty":    {promoLink, OutLinkPreview{URL: promoLink, Image: &OutMedia{}}, nil, CodeInvalid},
		"broken jpeg bytes": {promoLink, OutLinkPreview{URL: promoLink, Image: &OutMedia{Data: append([]byte("\xff\xd8\xff\xe0"), make([]byte, 32)...)}}, nil, CodeInvalid},
	} {
		h := newOutbox(t)
		p := tc.p
		_, err := h.ob.Send(context.Background(), SendRequest{Chat: viniJID.String(), Text: tc.text, Media: tc.media, LinkPreview: &p})
		var se *SendError
		if !errors.As(err, &se) || se.Code != tc.code {
			t.Errorf("%s: err=%v, want %s", name, err, tc.code)
		}
		if h.sender.sends() != 0 {
			t.Errorf("%s: sent anyway", name)
		}
	}
}

func TestBuildLinkMessage(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	p := &LinkPreview{URL: promoLink, Title: "Top Selene", Thumbnail: []byte{1}, Width: 1000, Height: 1000}
	up := &UploadedMedia{DirectPath: "/v/t62/x", MediaKey: []byte{2}, FileSHA256: []byte{3}, FileEncSHA256: []byte{4}}
	et := buildLinkMessage("x "+promoLink, p, up, now).GetExtendedTextMessage()
	if et.GetText() != "x "+promoLink || et.GetMatchedText() != promoLink || et.GetTitle() != "Top Selene" || et.Description != nil {
		t.Fatalf("text fields %+v", et)
	}
	if et.GetThumbnailDirectPath() != "/v/t62/x" || et.GetMediaKeyTimestamp() != now.Unix() || et.GetThumbnailWidth() != 1000 ||
		!bytes.Equal(et.GetThumbnailEncSHA256(), []byte{4}) || !bytes.Equal(et.GetJPEGThumbnail(), []byte{1}) {
		t.Fatalf("thumbnail fields %+v", et)
	}
	// Without an upload the card carries only the inline thumbnail.
	et = buildLinkMessage(promoLink, p, nil, now).GetExtendedTextMessage()
	if et.ThumbnailDirectPath != nil || et.MediaKey != nil || len(et.GetJPEGThumbnail()) != 1 {
		t.Fatalf("no-upload fields %+v", et)
	}
}

func TestAPISendLinkPreview(t *testing.T) {
	f, h := newSendFixture(t)
	scopes := "whatsapp.send:" + promoA
	img := base64.StdEncoding.EncodeToString(testJPEG(t, 500, 500))
	var res SendResult
	code := f.call(t, APISendPath, "promo-manager", scopes, SendAPIRequest{Chat: promoA, Text: "Top 62% OFF " + promoLink, IdempotencyKey: "lp-1",
		LinkPreview: &SendAPILinkPreview{URL: promoLink, Title: "Top Selene", Image: &SendAPIMedia{Data: img}}}, &res)
	if code != 200 || res.Kind != SendKindText || len(h.sender.links) != 1 || h.sender.links[0].up == nil {
		t.Fatalf("send → %d %+v links=%+v", code, res, h.sender.links)
	}
	var e apiErrorBody
	code = f.call(t, APISendPath, "promo-manager", scopes, SendAPIRequest{Chat: promoA, Text: promoLink,
		LinkPreview: &SendAPILinkPreview{URL: promoLink, Image: &SendAPIMedia{Data: "%%%"}}}, &e)
	if code != 400 || e.Error.Code != "invalid" {
		t.Fatalf("bad image → %d %+v", code, e)
	}
	e = apiErrorBody{}
	code = f.call(t, APISendPath, "promo-manager", scopes, SendAPIRequest{Chat: promoA, Text: promoLink,
		LinkPreview: &SendAPILinkPreview{URL: promoLink, Image: &SendAPIMedia{UploadID: "up_nope"}}}, &e)
	if code != 404 || e.Error.Code != "unknown_upload" {
		t.Fatalf("unknown upload → %d %+v", code, e)
	}
}
