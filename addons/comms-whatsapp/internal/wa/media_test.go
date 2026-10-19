package wa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// fakeFetcher writes payload, or fails with err.
type fakeFetcher struct {
	payload []byte
	err     error
	calls   int
}

func (f *fakeFetcher) Fetch(_ context.Context, _ whatsmeow.DownloadableMessage, out *os.File) error {
	f.calls++
	if f.err != nil {
		_, _ = out.Write([]byte("partial"))
		return f.err
	}
	_, err := out.Write(f.payload)
	return err
}

func imageMsg(caption string, size uint64) *waE2E.Message {
	sum := sha256.Sum256([]byte("jpeg bytes"))
	return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String(caption), Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(size),
		DirectPath: proto.String("/v/t62/abc"), MediaKey: []byte("k"), FileSHA256: sum[:], FileEncSHA256: []byte("e"),
	}}
}

type mediaHarness struct {
	*harness
	fetch *fakeFetcher
	cfg   MediaConfig
	sent  []Event
	paths []string
}

// newMediaHarness wires a handler whose downloads run synchronously and
// whose routes record what they deliver.
func newMediaHarness(t *testing.T) *mediaHarness {
	t.Helper()
	mh := &mediaHarness{harness: newHarness(t), fetch: &fakeFetcher{payload: []byte("jpeg bytes")},
		cfg: DefaultConfig().Media}
	d := NewDownloader(mh.arch, mh.fetch, slog.New(yardlogs.NopHandler()))
	mh.h.Media = func() MediaConfig { return mh.cfg }
	mh.h.Download = func(j MediaJob) bool {
		d.process(context.Background(), j)
		j.Done()
		return true
	}
	mh.h.Routes = func() Routes { return Routes{Default: "/wa/in"} }
	mh.h.Deliver = func(path string, ev Event) bool {
		mh.sent, mh.paths = append(mh.sent, ev), append(mh.paths, path)
		return true
	}
	return mh
}

func (mh *mediaHarness) receive(id string, m *waE2E.Message) {
	mh.h.Handle(context.Background(), message(types.MessageSource{Chat: family, Sender: momPN, IsGroup: true}, id, m))
}

func TestWatchedImageIsStoredThenRoutedWithItsPath(t *testing.T) {
	mh := newMediaHarness(t)
	mh.watch(t, family, "Família", archive.KindGroup)
	mh.receive("i1", imageMsg("cardápio", 10))

	h := mh.history(t, family)
	if len(h) != 1 || h[0].Media == nil {
		t.Fatalf("history %+v", h)
	}
	m := h[0].Media
	if m.Status != archive.MediaStored || !strings.HasSuffix(m.Path, ".jpg") || m.MimeType != "image/jpeg" {
		t.Fatalf("media %+v", m)
	}
	raw, err := os.ReadFile(m.Path)
	if err != nil || string(raw) != "jpeg bytes" {
		t.Fatalf("file %q %v", raw, err)
	}
	if info, _ := os.Stat(m.Path); info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", info.Mode())
	}
	if info, _ := os.Stat(filepath.Dir(m.Path)); info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", info.Mode())
	}
	if len(mh.sent) != 1 {
		t.Fatalf("deliveries %d", len(mh.sent))
	}
	md := mh.sent[0].Metadata
	if md["media_status"] != "stored" || md["media_path"] != m.Path || md["media_mime_type"] != "image/jpeg" ||
		md["kind"] != "image" || mh.sent[0].Body != "cardápio" {
		t.Fatalf("event metadata %+v body %q", md, mh.sent[0].Body)
	}
	// No temporary files are left behind.
	entries, _ := os.ReadDir(mh.arch.MediaDir())
	if len(entries) != 1 {
		t.Fatalf("media dir has %d entries", len(entries))
	}
}

func TestExtensionAndTypeFollowTheBytes(t *testing.T) {
	mh := newMediaHarness(t)
	mh.watch(t, family, "Família", archive.KindGroup)
	// Declared PNG, delivered JPEG (WhatsApp re-encodes).
	mh.fetch.payload = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("rest of a jpeg")...)
	m := imageMsg("", 10)
	m.ImageMessage.Mimetype = proto.String("image/png")
	mh.receive("p1", m)
	md := mh.history(t, family)[0].Media
	if !strings.HasSuffix(md.Path, ".jpg") || md.MimeType != "image/jpeg" {
		t.Fatalf("media %+v", md)
	}
	// Bytes of an unknown type keep the declared extension (Ogg is not
	// in the sniff table as audio).
	mh.fetch.payload = []byte("OggS\x00 opus")
	mh.receive("a1", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("audio/ogg; codecs=opus"),
		DirectPath: proto.String("/v/x"), FileLength: proto.Uint64(5)}})
	for _, m := range mh.history(t, family) {
		if m.ID == "a1" {
			md = m.Media
		}
	}
	if !strings.HasSuffix(md.Path, ".ogg") || md.MimeType != "audio/ogg; codecs=opus" {
		t.Fatalf("audio media %+v", md)
	}
}

func TestMediaPolicySkipsAndUnwatchedChatsNeverDownload(t *testing.T) {
	mh := newMediaHarness(t)
	mh.watch(t, family, "Família", archive.KindGroup)

	mh.cfg.MaxBytes = 5
	mh.receive("big", imageMsg("", 10))
	mh.cfg = MediaConfig{Download: []string{"audio"}, MaxBytes: DefaultMediaMaxBytes}
	mh.receive("off", imageMsg("", 1))
	if mh.fetch.calls != 0 {
		t.Fatalf("fetched %d times", mh.fetch.calls)
	}
	h := mh.history(t, family)
	if len(h) != 2 || h[0].Media.Status != archive.MediaSkipped || !strings.Contains(h[0].Media.Reason, "media-max-size") ||
		h[1].Media.Status != archive.MediaSkipped || !strings.Contains(h[1].Media.Reason, "media-download") {
		t.Fatalf("history %+v / %+v", h[0].Media, h[1].Media)
	}
	if len(mh.sent) != 2 || mh.sent[0].Metadata["media_status"] != "skipped" || mh.sent[0].Metadata["media_path"] != "" {
		t.Fatalf("events %+v", mh.sent)
	}

	// A routed but unwatched chat: delivered, never downloaded.
	mh.cfg = DefaultConfig().Media
	mh.h.Handle(context.Background(), message(types.MessageSource{Chat: momPN, Sender: momPN}, "dm1", imageMsg("", 1)))
	if mh.fetch.calls != 0 || len(mh.sent) != 3 || mh.sent[2].Metadata["media_reason"] != "chat is not watched" {
		t.Fatalf("unwatched: calls=%d events=%+v", mh.fetch.calls, mh.sent)
	}
}

func TestFailedDownloadIsRecordedAndStillRouted(t *testing.T) {
	mh := newMediaHarness(t)
	mh.watch(t, family, "Família", archive.KindGroup)
	mh.fetch.err = errors.New("410 gone")
	mh.receive("i1", imageMsg("", 10))
	h := mh.history(t, family)
	if h[0].Media.Status != archive.MediaFailed || !strings.Contains(h[0].Media.Reason, "410 gone") || h[0].Media.Path != "" {
		t.Fatalf("media %+v", h[0].Media)
	}
	if len(mh.sent) != 1 || mh.sent[0].Metadata["media_status"] != "failed" {
		t.Fatalf("events %+v", mh.sent)
	}
	if entries, _ := os.ReadDir(mh.arch.MediaDir()); len(entries) != 0 {
		t.Fatalf("partial file left: %v", entries)
	}
}

func TestFullDownloadQueueFailsAndRoutesAtOnce(t *testing.T) {
	mh := newMediaHarness(t)
	mh.watch(t, family, "Família", archive.KindGroup)
	mh.h.Download = func(MediaJob) bool { return false }
	mh.receive("i1", imageMsg("", 10))
	h := mh.history(t, family)
	if h[0].Media.Status != archive.MediaFailed || h[0].Media.Reason != "download queue full" || len(mh.sent) != 1 {
		t.Fatalf("media %+v events %d", h[0].Media, len(mh.sent))
	}
}

func TestRevokeAndUnwatchDeleteFiles(t *testing.T) {
	mh := newMediaHarness(t)
	mh.watch(t, family, "Família", archive.KindGroup)
	mh.receive("i1", imageMsg("", 10))
	mh.receive("i2", imageMsg("", 10))
	h := mh.history(t, family)
	p1, p2 := h[0].Media.Path, h[1].Media.Path
	if p1 == p2 || p1 == "" {
		t.Fatalf("paths %q %q", p1, p2)
	}
	mh.h.Handle(context.Background(), message(types.MessageSource{Chat: family, Sender: momPN, IsGroup: true}, "r1", revokeOf("i1")))
	if _, err := os.Stat(p1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("revoked file still there: %v", err)
	}
	if _, err := mh.arch.Unwatch(context.Background(), family.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p2); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unwatched file still there: %v", err)
	}
}

func TestRedeliveryKeepsStoredFile(t *testing.T) {
	mh := newMediaHarness(t)
	mh.watch(t, family, "Família", archive.KindGroup)
	mh.receive("i1", imageMsg("", 10))
	mh.receive("i1", imageMsg("", 10))
	if mh.fetch.calls != 1 {
		t.Fatalf("fetched %d times", mh.fetch.calls)
	}
	if h := mh.history(t, family); h[0].Media.Status != archive.MediaStored {
		t.Fatalf("media %+v", h[0].Media)
	}
}

func TestDownloaderRunCallsDoneAndDropsWhenFull(t *testing.T) {
	hs := newHarness(t)
	hs.watch(t, family, "Família", archive.KindGroup)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ref := ExtractContent(imageMsg("", 3)).Media
	info := ref.Info
	info.Status = archive.MediaPending
	_, err := hs.arch.InsertMessage(ctx, archive.Message{ChatJID: family.String(), ID: "i1", SenderJID: momPN.String(),
		Timestamp: ts, Kind: "image", Media: &info})
	mustDo(t, err)

	d := NewDownloader(hs.arch, &fakeFetcher{payload: []byte("abc")}, slog.New(yardlogs.NopHandler()))
	done := make(chan struct{})
	if !d.Enqueue(MediaJob{ChatJID: family.String(), MsgID: "i1", Ref: ref, Done: func() { close(done) }}) {
		t.Fatal("enqueue refused")
	}
	go d.Run(ctx)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job never finished")
	}
	if stored, failed := d.Counts(); stored != 1 || failed != 0 {
		t.Fatalf("counts %d/%d", stored, failed)
	}

	full := &Downloader{queue: make(chan MediaJob)}
	if full.Enqueue(MediaJob{}) {
		t.Fatal("enqueue on a full queue succeeded")
	}
}

func TestPruneAndSweepRemoveMediaFiles(t *testing.T) {
	mh := newMediaHarness(t)
	mh.watch(t, family, "Família", archive.KindGroup)
	mh.receive("i1", imageMsg("", 10))
	p := mh.history(t, family)[0].Media.Path

	// An orphan (crash leftover) and a fresh file of a download in flight.
	orphan := filepath.Join(mh.arch.MediaDir(), "orphan.jpg")
	young := filepath.Join(mh.arch.MediaDir(), ".young.part")
	mustDo(t, os.WriteFile(orphan, []byte("x"), 0o600))
	mustDo(t, os.WriteFile(young, []byte("x"), 0o600))
	old := time.Now().Add(-time.Hour)
	mustDo(t, os.Chtimes(orphan, old, old))
	if n, err := mh.arch.SweepMedia(context.Background(), MediaSweepGrace); err != nil || n != 1 {
		t.Fatalf("sweep n=%d err=%v", n, err)
	}
	for f, want := range map[string]bool{orphan: false, young: true, p: true} {
		if _, err := os.Stat(f); (err == nil) != want {
			t.Fatalf("%s exists=%v want %v", f, err == nil, want)
		}
	}

	// TTL expiry deletes the file with its message.
	mh.arch.WithClock(func() time.Time { return ts.Add(30 * 24 * time.Hour) })
	if n, err := mh.arch.Prune(context.Background(), DefaultTTL); err != nil || n != 1 {
		t.Fatalf("prune n=%d err=%v", n, err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned file still there: %v", err)
	}
}

func TestAPIMediaChunksAndScopes(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("0123456789"), (APIMediaChunk/10)+7) // just over one chunk
	sum := sha256.Sum256(payload)
	info := archive.Media{MimeType: "video/mp4", Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:]), Status: archive.MediaPending}
	_, err := f.arch.InsertMessage(ctx, archive.Message{ChatJID: promoA, ID: "v1", SenderJID: viniJID.String(), Timestamp: time.Now(),
		Kind: "video", Media: &info})
	mustDo(t, err)

	var e apiErrorBody
	if code := f.call(t, APIMediaPath, "c", "whatsapp.read:"+promoA, MediaRequest{Chat: promoA, ID: "v1"}, &e); code != http.StatusConflict ||
		e.Error.Code != "media_pending" {
		t.Fatalf("pending: %d %+v", code, e)
	}

	mustDo(t, os.MkdirAll(f.arch.MediaDir(), 0o700))
	name := MediaFileName(promoA, "v1", ".mp4")
	mustDo(t, os.WriteFile(filepath.Join(f.arch.MediaDir(), name), payload, 0o600))
	if ok, err := f.arch.SetMediaStored(ctx, promoA, "v1", name, ""); err != nil || !ok {
		t.Fatalf("stored %v %v", ok, err)
	}

	// Another chat's client cannot read it, and learns nothing.
	if code := f.call(t, APIMediaPath, "c", "whatsapp.read:"+promoB, MediaRequest{Chat: promoA, ID: "v1"}, nil); code != http.StatusForbidden {
		t.Fatalf("other chat: %d", code)
	}
	var got []byte
	var off int64
	for i := 0; ; i++ {
		var r MediaResponse
		if code := f.call(t, APIMediaPath, "c", "whatsapp.read:"+promoA, MediaRequest{Chat: promoA, ID: "v1", Offset: off}, &r); code != http.StatusOK {
			t.Fatalf("chunk %d: %d", i, code)
		}
		if r.Size != int64(len(payload)) || r.MimeType != "video/mp4" || r.Offset != off || r.Length != int64(len(r.Data)) {
			t.Fatalf("chunk %d: %+v", i, r)
		}
		got = append(got, r.Data...)
		off += r.Length
		if r.EOF {
			if i != 1 {
				t.Fatalf("eof after %d chunks", i+1)
			}
			break
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("reassembled file differs")
	}
	if code := f.call(t, APIMediaPath, "c", "whatsapp.read:"+promoA, MediaRequest{Chat: promoA, ID: "nope"}, &e); code != http.StatusNotFound ||
		e.Error.Code != "no_media" {
		t.Fatalf("no media: %d %+v", code, e)
	}
	if code := f.call(t, APIMediaPath, "c", "whatsapp.read:"+promoA, MediaRequest{Chat: promoA, ID: "v1", Offset: int64(len(payload)) + 1}, nil); code != http.StatusBadRequest {
		t.Fatalf("offset past end: %d", code)
	}
}

func TestMediaConfigKeys(t *testing.T) {
	c := DefaultConfig()
	if !c.Media.Wants("image") || c.Media.Wants("sticker") || c.Media.MaxBytes != 25<<20 {
		t.Fatalf("defaults %+v", c.Media)
	}
	mustDo(t, c.Set("media-download", "audio, sticker,audio"))
	if strings.Join(c.Media.Download, ",") != "audio,sticker" {
		t.Fatalf("download %v", c.Media.Download)
	}
	mustDo(t, c.Set("media-download", "off"))
	if c.Media.Wants("audio") || c.Media.Download == nil {
		t.Fatalf("off: %#v", c.Media.Download)
	}
	if bad := c; bad.Set("media-download", "image,gif") == nil {
		t.Fatal("unknown kind accepted")
	}
	mustDo(t, c.Set("media-max-size", "50MB"))
	if c.Media.MaxBytes != 50<<20 {
		t.Fatalf("max %d", c.Media.MaxBytes)
	}
	for _, bad := range []string{"200MB", "0", "lots"} {
		if cc := c; cc.Set("media-max-size", bad) == nil {
			t.Fatalf("media-max-size %q accepted", bad)
		}
	}

	// A config.json written before media existed gets the defaults.
	path := filepath.Join(t.TempDir(), "config.json")
	mustDo(t, os.WriteFile(path, []byte(`{"version":1,"device_name":"x","default_ttl":"14d",
		"limits":{"per_chat":"6/10m","per_group":"20/1h","global":"60/1h"},"max_chars":100}`), 0o600))
	old, err := LoadConfig(path)
	if err != nil || !old.Media.Wants("video") || old.Media.MaxBytes != DefaultMediaMaxBytes {
		t.Fatalf("old config %+v %v", old.Media, err)
	}
}

func TestParseAndFormatBytes(t *testing.T) {
	for in, want := range map[string]int64{"25MB": 25 << 20, "512kb": 512 << 10, "1g": 1 << 30, "1048576": 1 << 20, "1.5 MB": 3 << 19} {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Fatalf("ParseBytes(%q) = %d, %v", in, got, err)
		}
	}
	if _, err := ParseBytes("-1"); err == nil {
		t.Fatal("negative accepted")
	}
	if FormatBytes(25<<20) != "25.0 MB" || FormatBytes(900) != "900 B" || FormatBytes(2048) != "2 KB" {
		t.Fatal(FormatBytes(25<<20), FormatBytes(900), FormatBytes(2048))
	}
}

func TestMediaExtAndFileName(t *testing.T) {
	for _, c := range []struct{ mime, name, want string }{
		{"image/jpeg", "", ".jpg"},
		{"audio/ogg; codecs=opus", "", ".ogg"},
		{"application/vnd.ms-excel", "planilha.XLS", ".xls"},
		{"application/octet-stream", "../../etc/passwd", ".bin"},
		{"", "evil.sh;rm", ".bin"},
	} {
		if got := mediaExt(c.mime, c.name); got != c.want {
			t.Fatalf("mediaExt(%q, %q) = %q", c.mime, c.name, got)
		}
	}
	a, b := MediaFileName("x@g.us", "1", ".jpg"), MediaFileName("x@g.us", "2", ".jpg")
	if a == b || strings.ContainsAny(a, "/@") || !strings.HasSuffix(a, ".jpg") || a != MediaFileName("x@g.us", "1", ".jpg") {
		t.Fatalf("names %q %q", a, b)
	}
}

func revokeOf(id string) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String(id)},
	}}
}
