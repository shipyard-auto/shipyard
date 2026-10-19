package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// mediaColumns are the media table's columns as read alongside a message
// (alias d, LEFT JOINed: all NULL when the message has no attachment).
const mediaColumns = `d.status, COALESCE(d.mime_type, ''), COALESCE(d.size, 0), COALESCE(d.seconds, 0),
	COALESCE(d.width, 0), COALESCE(d.height, 0), COALESCE(d.voice_note, 0), COALESCE(d.file_name, ''),
	COALESCE(d.sha256, ''), COALESCE(d.reason, ''), COALESCE(d.file, '')`

// mediaRow scans mediaColumns.
type mediaRow struct {
	status    sql.NullString
	m         Media
	voiceNote int
	file      string
}

func (r *mediaRow) dest() []any {
	return []any{&r.status, &r.m.MimeType, &r.m.Size, &r.m.Seconds, &r.m.Width, &r.m.Height, &r.voiceNote,
		&r.m.FileName, &r.m.SHA256, &r.m.Reason, &r.file}
}

// media returns the scanned attachment, or nil when the row had none.
func (r *mediaRow) media(dir string) *Media {
	if !r.status.Valid {
		return nil
	}
	m := r.m
	m.Status = MediaStatus(r.status.String)
	m.VoiceNote = r.voiceNote == 1
	if m.Status == MediaStored && r.file != "" {
		m.Path = filepath.Join(dir, r.file)
	}
	return &m
}

// MediaDir is where attachment files live: media/ next to archive.db.
func (s *Store) MediaDir() string { return s.mediaDir }

// GetMedia returns the attachment of one archived message.
func (s *Store) GetMedia(ctx context.Context, chatJID, id string) (Media, error) {
	var r mediaRow
	err := s.db.QueryRowContext(ctx, `SELECT `+mediaColumns+` FROM media d WHERE d.chat_jid = ? AND d.msg_id = ?`,
		chatJID, id).Scan(r.dest()...)
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, fmt.Errorf("%w: %s/%s", ErrNoMedia, chatJID, id)
	}
	if err != nil {
		return Media{}, fmt.Errorf("archive: media %s/%s: %w", chatJID, id, err)
	}
	return *r.media(s.mediaDir), nil
}

// SetMediaStored records that the attachment is on disk as file (a name
// under MediaDir). mimeType, when not empty, replaces the declared type
// (the bytes turned out to be another). It reports false when the message
// no longer exists (pruned, unwatched or revoked while downloading): the
// caller then owns the file and must delete it.
func (s *Store) SetMediaStored(ctx context.Context, chatJID, id, file, mimeType string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE media SET status = 'stored', reason = NULL, file = ?,
		mime_type = COALESCE(?, mime_type) WHERE chat_jid = ? AND msg_id = ?`,
		file, nullable(mimeType), chatJID, id)
	if err != nil {
		return false, fmt.Errorf("archive: media %s/%s: %w", chatJID, id, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetMediaStatus records a pending, skipped or failed attachment.
func (s *Store) SetMediaStatus(ctx context.Context, chatJID, id string, status MediaStatus, reason string) error {
	if status == MediaStored {
		return errors.New("archive: use SetMediaStored for stored media")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE media SET status = ?, reason = ?, file = NULL WHERE chat_jid = ? AND msg_id = ?`,
		string(status), nullable(reason), chatJID, id)
	if err != nil {
		return fmt.Errorf("archive: media %s/%s: %w", chatJID, id, err)
	}
	return nil
}

// FailPendingMedia marks every pending download as failed. The daemon
// calls it at start: the queue lived in memory, and the download keys
// with it, so nothing left pending will ever finish.
func (s *Store) FailPendingMedia(ctx context.Context, reason string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE media SET status = 'failed', reason = ? WHERE status = 'pending'`, nullable(reason))
	if err != nil {
		return 0, fmt.Errorf("archive: fail pending media: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SweepMedia deletes files in MediaDir that no media row points to and
// that were last modified more than grace ago (younger files may belong
// to a download about to be recorded). Returns how many were removed.
func (s *Store) SweepMedia(ctx context.Context, grace time.Duration) (int, error) {
	entries, err := os.ReadDir(s.mediaDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("archive: sweep media: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT file FROM media WHERE file IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("archive: sweep media: %w", err)
	}
	known := map[string]bool{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("archive: sweep media: %w", err)
		}
		known[f] = true
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("archive: sweep media: %w", err)
	}
	cutoff := s.now().Add(-grace)
	n := 0
	var errs []error
	for _, e := range entries {
		if known[e.Name()] || e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.mediaDir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// mediaFiles lists the stored files of the media rows matching where.
func mediaFiles(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT file FROM media WHERE file IS NOT NULL AND `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// removeMediaFiles deletes attachment files whose rows are gone. A file
// that cannot be removed is reported; SweepMedia retries it later.
func (s *Store) removeMediaFiles(files []string) error {
	var errs []error
	for _, f := range files {
		if err := os.Remove(filepath.Join(s.mediaDir, filepath.Base(f))); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("archive: remove media files: %w", err)
	}
	return nil
}
