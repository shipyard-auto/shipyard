package wa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 40, 20))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var (
	testMP4 = append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 64)...)
	testOgg = append([]byte("OggS\x00\x02"), make([]byte, 64)...)
	testPDF = []byte("%PDF-1.4\n%âãÏÓ\n1 0 obj\n<<>>\nendobj\n")
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	mustDo(t, os.WriteFile(p, data, 0o600))
	return p
}

func TestPrepareMediaClassifies(t *testing.T) {
	jpg := testJPEG(t, 400, 200)
	for name, tc := range map[string]struct {
		in              OutMedia
		kind, mime, doc string
	}{
		"jpeg by content":        {OutMedia{Data: jpg}, SendKindImage, "image/jpeg", ""},
		"jpeg by name":           {OutMedia{Path: writeTemp(t, "promo.jpg", jpg)}, SendKindImage, "image/jpeg", "promo.jpg"},
		"png declared as jpeg":   {OutMedia{Data: testPNG(t), MimeType: "image/jpeg"}, SendKindImage, "image/png", ""},
		"mp4":                    {OutMedia{Path: writeTemp(t, "clip.mp4", testMP4)}, SendKindVideo, "video/mp4", "clip.mp4"},
		"ogg voice note":         {OutMedia{Data: testOgg, VoiceNote: true}, SendKindAudio, "audio/ogg; codecs=opus", ""},
		"mp3 by mime":            {OutMedia{Data: []byte("ID3\x03\x00\x00\x00"), MimeType: "audio/mpeg"}, SendKindAudio, "audio/mpeg", ""},
		"pdf document":           {OutMedia{Path: writeTemp(t, "tabela.pdf", testPDF)}, SendKindFile, "application/pdf", "tabela.pdf"},
		"renamed document":       {OutMedia{Path: writeTemp(t, "x.pdf", testPDF), FileName: "Preços.pdf"}, SendKindFile, "application/pdf", "Preços.pdf"},
		"unknown bytes are file": {OutMedia{Data: []byte{0x01, 0x02, 0x03}}, SendKindFile, "application/octet-stream", ""},
	} {
		m, se := prepareMedia(&tc.in, DefaultMediaSendMaxBytes)
		if se != nil {
			t.Errorf("%s: %v", name, se)
			continue
		}
		if m.Kind != tc.kind || m.MimeType != tc.mime || m.FileName != tc.doc {
			t.Errorf("%s: kind=%s mime=%s name=%q", name, m.Kind, m.MimeType, m.FileName)
		}
		sum := sha256.Sum256(m.Data)
		if m.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("%s: sha256", name)
		}
	}

	m, _ := prepareMedia(&OutMedia{Data: jpg}, DefaultMediaSendMaxBytes)
	if m.Width != 400 || m.Height != 200 || len(m.Thumbnail) == 0 {
		t.Fatalf("image dims=%dx%d thumb=%d", m.Width, m.Height, len(m.Thumbnail))
	}
	th, err := jpeg.DecodeConfig(bytes.NewReader(m.Thumbnail))
	if err != nil || th.Width != thumbMaxSide || th.Height != thumbMaxSide/2 {
		t.Fatalf("thumbnail=%+v %v", th, err)
	}
}

func TestPrepareMediaRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		in   OutMedia
		max  int64
		code SendCode
	}{
		"text posing as image": {OutMedia{Data: []byte("hello"), MimeType: "image/png"}, 1 << 20, CodeInvalid},
		"text posing as video": {OutMedia{Path: writeTemp(t, "x.mp4", []byte("not a video"))}, 1 << 20, CodeInvalid},
		"voice note not ogg":   {OutMedia{Data: testJPEG(t, 8, 8), VoiceNote: true}, 1 << 20, CodeInvalid},
		"too large data":       {OutMedia{Data: make([]byte, 2048)}, 1024, CodeTooLong},
		"too large file":       {OutMedia{Path: writeTemp(t, "big.bin", make([]byte, 2048))}, 1024, CodeTooLong},
		"relative path":        {OutMedia{Path: "promo.jpg"}, 1 << 20, CodeInvalid},
		"missing file":         {OutMedia{Path: "/nonexistent/promo.jpg"}, 1 << 20, CodeInvalid},
		"empty file":           {OutMedia{Path: writeTemp(t, "e.jpg", nil)}, 1 << 20, CodeInvalid},
		"path and data":        {OutMedia{Path: "/x", Data: []byte{1}}, 1 << 20, CodeInvalid},
		"nothing":              {OutMedia{}, 1 << 20, CodeInvalid},
	} {
		if _, se := prepareMedia(&tc.in, tc.max); se == nil || se.Code != tc.code {
			t.Errorf("%s: %v", name, se)
		}
	}
}

func TestOutboxSendsMediaThroughPolicy(t *testing.T) {
	h := newOutbox(t)
	ctx := context.Background()
	mustDo(t, h.arch.Watch(ctx, viniJID.String(), 0))
	jpg := testJPEG(t, 64, 64)

	res, err := h.ob.Send(ctx, SendRequest{Chat: "Vini Eduqo", Text: "  Ração 30% off  ", Media: &OutMedia{Data: jpg}, IdempotencyKey: "k1"})
	if err != nil || res.Kind != SendKindImage || res.MsgID != "MSG1" {
		t.Fatalf("send=%+v err=%v", res, err)
	}
	if h.sender.uploads != 1 || len(h.sender.media) != 1 || h.sender.media[0].Caption != "Ração 30% off" {
		t.Fatalf("uploads=%d media=%+v", h.sender.uploads, h.sender.media)
	}
	if len(h.slept) != 1 {
		t.Fatal("humanize skipped for media")
	}

	// Audited with its kind; the replayed key reports it too.
	log, _ := h.arch.ListSent(ctx, t0Send.Add(-time.Hour), "", 0)
	if len(log) != 1 || log[0].Kind != SendKindImage {
		t.Fatalf("audit=%+v", log)
	}
	again, _ := h.ob.Send(ctx, SendRequest{Chat: "Vini Eduqo", Media: &OutMedia{Data: jpg}, IdempotencyKey: "k1"})
	if !again.Duplicate || again.Kind != SendKindImage || h.sender.uploads != 1 {
		t.Fatalf("replay=%+v uploads=%d", again, h.sender.uploads)
	}

	// The watched chat keeps a copy, like a received attachment.
	hist, _ := h.arch.History(ctx, viniJID.String(), time.Time{}, 0)
	if len(hist) != 1 || hist[0].Kind != SendKindImage || hist[0].Media == nil || hist[0].Media.Status != archive.MediaStored {
		t.Fatalf("history=%+v", hist)
	}
	kept, err := os.ReadFile(hist[0].Media.Path)
	if err != nil || !bytes.Equal(kept, jpg) {
		t.Fatalf("kept copy: %v", err)
	}

	// Same bytes again (a broadcast): no second upload; the cache expires.
	h.now = h.now.Add(20 * time.Minute)
	if _, err := h.ob.Send(ctx, SendRequest{Chat: "Vini Eduqo", Media: &OutMedia{Data: jpg}}); err != nil {
		t.Fatal(err)
	}
	if h.sender.uploads != 1 {
		t.Fatalf("re-uploaded within the cache window: %d", h.sender.uploads)
	}
	h.now = h.now.Add(uploadCacheTTL + time.Minute)
	if _, err := h.ob.Send(ctx, SendRequest{Chat: "Vini Eduqo", Media: &OutMedia{Data: jpg}}); err != nil {
		t.Fatal(err)
	}
	if h.sender.uploads != 2 {
		t.Fatalf("expired upload reused: %d", h.sender.uploads)
	}
}

func TestOutboxMediaRefusals(t *testing.T) {
	h := newOutbox(t)
	ctx := context.Background()
	var se *SendError
	_, err := h.ob.Send(ctx, SendRequest{Chat: "Vini Eduqo", Text: "legenda", Media: &OutMedia{Data: testOgg, VoiceNote: true}})
	if !errors.As(err, &se) || se.Code != CodeInvalid || !strings.Contains(se.Msg, "caption") {
		t.Fatalf("audio caption: %v", err)
	}
	h.cfg.Media.SendMaxBytes = 10
	_, err = h.ob.Send(ctx, SendRequest{Chat: "Vini Eduqo", Media: &OutMedia{Data: testJPEG(t, 8, 8)}})
	if !errors.As(err, &se) || se.Code != CodeTooLong || !strings.Contains(se.Msg, "media-send-max-size") {
		t.Fatalf("too large: %v", err)
	}
	h.cfg = DefaultConfig()
	// The policy still applies to media.
	h.cfg.Paused = true
	_, err = h.ob.Send(ctx, SendRequest{Chat: "Vini Eduqo", Media: &OutMedia{Data: testJPEG(t, 8, 8)}})
	if !errors.As(err, &se) || se.Code != CodePaused {
		t.Fatalf("paused: %v", err)
	}
	h.cfg.Paused = false
	res, err := h.ob.Send(ctx, SendRequest{Chat: "Vini Eduqo", Media: &OutMedia{Data: testPDF}, DryRun: true})
	if err != nil || !res.DryRun || res.Kind != SendKindFile || h.sender.uploads != 0 {
		t.Fatalf("dry run=%+v err=%v uploads=%d", res, err, h.sender.uploads)
	}
	// Unwatched chat: sent and audited, but no copy kept.
	if _, err := h.ob.Send(ctx, SendRequest{Chat: "Vini Eduqo", Media: &OutMedia{Data: testPDF}}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(h.arch.MediaDir()); len(entries) != 0 {
		t.Fatalf("kept a file for an unwatched chat: %d", len(entries))
	}
	// An upload failure is a send_failed refusal, nothing recorded.
	h.sender.fail = errors.New("cdn down")
	_, err = h.ob.Send(ctx, SendRequest{Chat: "Vini Eduqo", Media: &OutMedia{Data: testJPEG(t, 9, 9)}})
	if !errors.As(err, &se) || se.Code != CodeSendFailed || !strings.Contains(se.Msg, "upload") {
		t.Fatalf("upload failure: %v", err)
	}
	if log, _ := h.arch.ListSent(ctx, t0Send.Add(-time.Hour), "", 0); len(log) != 1 {
		t.Fatalf("audit after failure=%+v", log)
	}
}

func TestUploadStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "uploads")
	mustDo(t, os.MkdirAll(dir, 0o700))
	mustDo(t, os.WriteFile(filepath.Join(dir, "up_stale"), []byte("x"), 0o600))
	s, err := NewUploadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "up_stale")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("previous run's file kept")
	}
	now := t0Send
	s.Now = func() time.Time { return now }

	a, err := s.Append("promo", "", 0, []byte("hello "), 100)
	if err != nil || a.Size != 6 || !strings.HasPrefix(a.UploadID, "up_") {
		t.Fatalf("first=%+v %v", a, err)
	}
	if _, err := s.Append("promo", a.UploadID, 3, []byte("x"), 100); !errors.Is(err, ErrUploadOffset) {
		t.Fatalf("bad offset: %v", err)
	}
	b, err := s.Append("promo", a.UploadID, 6, []byte("world"), 100)
	sum := sha256.Sum256([]byte("hello world"))
	if err != nil || b.Size != 11 || b.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("second=%+v %v", b, err)
	}
	if _, err := s.Append("promo", a.UploadID, 11, make([]byte, 90), 100); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	// Uploads belong to their client.
	if _, err := s.Append("other", a.UploadID, 11, []byte("!"), 100); !errors.Is(err, ErrUploadUnknown) {
		t.Fatalf("foreign append: %v", err)
	}
	if _, err := s.Path("other", a.UploadID); !errors.Is(err, ErrUploadUnknown) {
		t.Fatalf("foreign path: %v", err)
	}
	p, err := s.Path("promo", a.UploadID)
	if got, _ := os.ReadFile(p); err != nil || string(got) != "hello world" {
		t.Fatalf("path=%q %v", got, err)
	}
	for range MaxUploadsPerClient - 1 {
		if _, err := s.Append("promo", "", 0, []byte("x"), 100); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Append("promo", "", 0, []byte("x"), 100); !errors.Is(err, ErrUploadTooMany) {
		t.Fatalf("too many: %v", err)
	}
	now = now.Add(UploadTTL + time.Minute)
	s.Sweep()
	if _, err := s.Path("promo", a.UploadID); !errors.Is(err, ErrUploadUnknown) {
		t.Fatal("expired upload still served")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("expired files kept: %d", len(entries))
	}
}
