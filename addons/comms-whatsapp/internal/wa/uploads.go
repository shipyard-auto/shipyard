package wa

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Uploads let API clients send files larger than one request can carry
// (fairway caps forwarded bodies at 4 MB): the file arrives in chunks of
// up to APIChunkMax, then /v1/send references it by upload_id. An upload
// belongs to the client that started it, is reusable (one upload, a
// broadcast to several groups) and expires UploadTTL after its last chunk.
// The registry is in memory; files left by a previous run are removed at
// start.

// Upload tuning.
const (
	APIChunkMax         = 2 << 20
	UploadTTL           = time.Hour
	MaxUploadsPerClient = 10
)

// Upload errors, mapped to API codes.
var (
	ErrUploadUnknown  = errors.New("unknown or expired upload_id")
	ErrUploadOffset   = errors.New("offset does not match the upload size")
	ErrUploadTooLarge = errors.New("upload exceeds media-send-max-size")
	ErrUploadTooMany  = errors.New("too many uploads in progress")
)

// UploadInfo describes an upload after a chunk.
type UploadInfo struct {
	UploadID  string    `json:"upload_id"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	ExpiresAt time.Time `json:"expires_at"`
}

// UploadStore holds uploads under Dir (0700).
type UploadStore struct {
	Dir string
	Now func() time.Time

	mu    sync.Mutex
	items map[string]*upload
}

type upload struct {
	client string
	path   string
	size   int64
	sum    hash.Hash
	last   time.Time
}

// NewUploadStore returns a store over dir, removing what a previous run
// left there.
func NewUploadStore(dir string) (*UploadStore, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("uploads: reset %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("uploads: create %s: %w", dir, err)
	}
	return &UploadStore{Dir: dir, Now: time.Now, items: map[string]*upload{}}, nil
}

// Append adds data at offset to the client's upload id; an empty id at
// offset 0 starts a new upload. On ErrUploadOffset the returned info holds
// the current size, so the client can resume.
func (s *UploadStore) Append(client, id string, offset int64, data []byte, maxBytes int64) (UploadInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	s.sweepLocked(now)
	var u *upload
	if id == "" {
		if offset != 0 {
			return UploadInfo{}, ErrUploadOffset
		}
		n := 0
		for _, it := range s.items {
			if it.client == client {
				n++
			}
		}
		if n >= MaxUploadsPerClient {
			return UploadInfo{}, ErrUploadTooMany
		}
		var b [12]byte
		_, _ = rand.Read(b[:])
		id = "up_" + hex.EncodeToString(b[:])
		f, err := os.OpenFile(filepath.Join(s.Dir, id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return UploadInfo{}, fmt.Errorf("uploads: create: %w", err)
		}
		_ = f.Close()
		u = &upload{client: client, path: filepath.Join(s.Dir, id), sum: sha256.New(), last: now}
		s.items[id] = u
	} else if u = s.items[id]; u == nil || u.client != client {
		return UploadInfo{}, ErrUploadUnknown
	}
	info := func() UploadInfo {
		return UploadInfo{UploadID: id, Size: u.size, SHA256: hex.EncodeToString(u.sum.Sum(nil)), ExpiresAt: u.last.Add(UploadTTL)}
	}
	if offset != u.size {
		return info(), ErrUploadOffset
	}
	if u.size+int64(len(data)) > maxBytes {
		return info(), ErrUploadTooLarge
	}
	f, err := os.OpenFile(u.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return UploadInfo{}, fmt.Errorf("uploads: open: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return UploadInfo{}, fmt.Errorf("uploads: write: %w", err)
	}
	if err := f.Close(); err != nil {
		return UploadInfo{}, fmt.Errorf("uploads: write: %w", err)
	}
	u.size += int64(len(data))
	u.sum.Write(data)
	u.last = now
	return info(), nil
}

// Path returns the file of the client's upload id.
func (s *UploadStore) Path(client, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.Now())
	u := s.items[id]
	if u == nil || u.client != client || u.size == 0 {
		return "", ErrUploadUnknown
	}
	return u.path, nil
}

// Sweep removes expired uploads.
func (s *UploadStore) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.Now())
}

func (s *UploadStore) sweepLocked(now time.Time) {
	for id, u := range s.items {
		if now.Sub(u.last) > UploadTTL {
			_ = os.Remove(u.path)
			delete(s.items, id)
		}
	}
}
