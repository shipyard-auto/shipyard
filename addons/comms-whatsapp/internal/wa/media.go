package wa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// Attachments of watched chats are downloaded by one background worker,
// so the WhatsApp event loop never waits on the CDN. The download keys
// travel with the job, in memory only: a job lost to a restart is marked
// failed at the next start (FailPendingMedia), never retried.

// Media download tuning.
const (
	MediaQueueSize       = 64
	MediaDownloadTimeout = 2 * time.Minute
	// MediaSweepGrace protects files of downloads still being recorded
	// from the orphan sweep.
	MediaSweepGrace = 10 * time.Minute
)

// MediaFetcher downloads and decrypts one attachment into f. An interface
// so the downloader can be tested without WhatsApp.
type MediaFetcher interface {
	Fetch(ctx context.Context, src whatsmeow.DownloadableMessage, f *os.File) error
}

// MediaJob is one attachment to download.
type MediaJob struct {
	ChatJID string
	MsgID   string
	Ref     *MediaRef
	// Done runs after the job ends, stored or not (not when the daemon
	// stops first). The handler routes the message from here, so agents
	// get the file together with the event.
	Done func()
}

// Downloader fetches attachments into the archive's media directory.
type Downloader struct {
	Archive *archive.Store
	Fetcher MediaFetcher
	Log     *slog.Logger
	Timeout time.Duration

	queue  chan MediaJob
	stored atomic.Int64
	failed atomic.Int64
}

// NewDownloader returns a Downloader with production defaults.
func NewDownloader(arch *archive.Store, f MediaFetcher, log *slog.Logger) *Downloader {
	return &Downloader{Archive: arch, Fetcher: f, Log: log, Timeout: MediaDownloadTimeout,
		queue: make(chan MediaJob, MediaQueueSize)}
}

// Enqueue schedules j without blocking; false means the queue is full.
func (d *Downloader) Enqueue(j MediaJob) bool {
	select {
	case d.queue <- j:
		return true
	default:
		return false
	}
}

// Counts returns how many files were stored and how many downloads failed
// since start.
func (d *Downloader) Counts() (stored, failed int64) { return d.stored.Load(), d.failed.Load() }

// Run drains the queue until ctx is done.
func (d *Downloader) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-d.queue:
			d.process(ctx, j)
			if j.Done != nil && ctx.Err() == nil {
				j.Done()
			}
		}
	}
}

func (d *Downloader) process(ctx context.Context, j MediaJob) {
	cur, err := d.Archive.GetMedia(ctx, j.ChatJID, j.MsgID)
	switch {
	case errors.Is(err, archive.ErrNoMedia):
		return // message deleted (revoked, unwatched) before its turn
	case err != nil:
		d.fail(ctx, j, err, "archive read failed")
		return
	case cur.Status == archive.MediaStored:
		if _, err := os.Stat(cur.Path); err == nil {
			return // redelivered message, file already here
		}
	}

	size, name, sniffed, err := d.download(ctx, j)
	if err != nil {
		reason := "download failed: " + err.Error()
		if ctx.Err() != nil {
			reason = "interrupted by a daemon stop"
		}
		d.fail(ctx, j, err, reason)
		return
	}
	ok, err := d.Archive.SetMediaStored(context.WithoutCancel(ctx), j.ChatJID, j.MsgID, name, sniffed)
	if err != nil || !ok {
		// The message went away while downloading; its file must too.
		_ = os.Remove(filepath.Join(d.Archive.MediaDir(), name))
		if err != nil {
			d.fail(ctx, j, err, "archive write failed")
		}
		return
	}
	d.stored.Add(1)
	d.Log.Info(yardlogs.EventWAMediaStored, slog.String(yardlogs.KeyMessage, "attachment stored"),
		slog.String(yardlogs.KeyChatJID, j.ChatJID), slog.String(yardlogs.KeyEntityID, j.MsgID),
		slog.Int64(yardlogs.KeySizeBytes, size))
}

// download writes the attachment to a temporary file in the media
// directory and renames it once complete and verified (whatsmeow checks
// both SHA-256 sums), so a half-written file never has a final name.
//
// The extension follows the bytes when they are a type we know: senders'
// apps declare one type and WhatsApp may re-encode (a "PNG" arrives as
// JPEG). sniffed is that detected type, "" when the declared one stands.
func (d *Downloader) download(ctx context.Context, j MediaJob) (size int64, name, sniffed string, err error) {
	dir := d.Archive.MediaDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, "", "", err
	}
	tmp, err := os.CreateTemp(dir, ".media.*.part") // mode 0600
	if err != nil {
		return 0, "", "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	ctx, cancel := context.WithTimeout(ctx, d.Timeout)
	defer cancel()
	if err := d.Fetcher.Fetch(ctx, j.Ref.Source, tmp); err != nil {
		_ = tmp.Close()
		return 0, "", "", err
	}
	info, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		return 0, "", "", err
	}
	head := make([]byte, 512)
	n, _ := tmp.ReadAt(head, 0)
	if err := tmp.Close(); err != nil {
		return 0, "", "", err
	}
	ext := mediaExt(j.Ref.Info.MimeType, j.Ref.Info.FileName)
	if t := http.DetectContentType(head[:n]); mimeExt[t] != "" && mimeExt[t] != ext {
		ext, sniffed = mimeExt[t], t
	}
	name = MediaFileName(j.ChatJID, j.MsgID, ext)
	return info.Size(), name, sniffed, os.Rename(tmpName, filepath.Join(dir, name))
}

func (d *Downloader) fail(ctx context.Context, j MediaJob, err error, reason string) {
	d.failed.Add(1)
	_ = d.Archive.SetMediaStatus(context.WithoutCancel(ctx), j.ChatJID, j.MsgID, archive.MediaFailed, truncateReason(reason))
	d.Log.Warn(yardlogs.EventWAMediaFailed, slog.String(yardlogs.KeyMessage, "attachment not stored"),
		slog.String(yardlogs.KeyChatJID, j.ChatJID), slog.String(yardlogs.KeyEntityID, j.MsgID),
		slog.String(yardlogs.KeyError, err.Error()))
}

func truncateReason(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// mediaDecision applies the download policy to a new attachment.
func mediaDecision(cfg MediaConfig, kind string, ref *MediaRef) (archive.MediaStatus, string) {
	switch {
	case !cfg.Wants(kind):
		return archive.MediaSkipped, fmt.Sprintf("%s downloads are off (config media-download)", kind)
	case ref.Info.Size > cfg.MaxBytes:
		return archive.MediaSkipped, fmt.Sprintf("%s is larger than media-max-size (%s)",
			FormatBytes(ref.Info.Size), FormatBytes(cfg.MaxBytes))
	case ref.Source == nil || ref.Source.GetDirectPath() == "":
		return archive.MediaFailed, "the message carries no download path"
	}
	return archive.MediaPending, ""
}

// MediaFileName is the deterministic name of a message's attachment file.
// It hashes the ids rather than embedding them, so no id can steer the
// path.
func MediaFileName(chatJID, msgID, ext string) string {
	sum := sha256.Sum256([]byte(chatJID + "\x00" + msgID))
	return hex.EncodeToString(sum[:12]) + ext
}

var mimeExt = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
	"video/mp4": ".mp4", "video/3gpp": ".3gp", "video/quicktime": ".mov",
	"audio/ogg": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a", "audio/aac": ".aac", "audio/amr": ".amr",
	"application/pdf": ".pdf",
}

// mediaExt picks a file extension: from the MIME type when known, else
// from a document's own file name, else ".bin".
func mediaExt(mimeType, fileName string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	if e, ok := mimeExt[strings.ToLower(strings.TrimSpace(base))]; ok {
		return e
	}
	e := strings.ToLower(filepath.Ext(fileName))
	if len(e) >= 2 && len(e) <= 10 && strings.Trim(e[1:], "abcdefghijklmnopqrstuvwxyz0123456789") == "" {
		return e
	}
	return ".bin"
}

// clientFetcher adapts *whatsmeow.Client to MediaFetcher.
type clientFetcher struct{ cli *whatsmeow.Client }

func (c clientFetcher) Fetch(ctx context.Context, src whatsmeow.DownloadableMessage, f *os.File) error {
	return c.cli.DownloadToFile(ctx, src, f)
}

// Fetcher returns the whatsmeow-backed MediaFetcher for this session.
func (s *Session) Fetcher() MediaFetcher { return clientFetcher{cli: s.Client} }
