package logs

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreAppendWritesDailyFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	at := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	if err := store.Append("cron", at, []byte(`{"event":"a"}`)); err != nil {
		t.Fatalf("append: %v", err)
	}

	path := filepath.Join(dir, "cron", "2026-04-24.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != `{"event":"a"}`+"\n" {
		t.Fatalf("unexpected file contents: %q", data)
	}
}

func TestStoreRotatesOnDayChange(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	day1 := time.Date(2026, 4, 24, 23, 59, 59, 0, time.UTC)
	day2 := time.Date(2026, 4, 25, 0, 0, 1, 0, time.UTC)
	if err := store.Append("cron", day1, []byte(`{"d":1}`)); err != nil {
		t.Fatalf("append d1: %v", err)
	}
	if err := store.Append("cron", day2, []byte(`{"d":2}`)); err != nil {
		t.Fatalf("append d2: %v", err)
	}

	for _, name := range []string{"2026-04-24.jsonl", "2026-04-25.jsonl"} {
		path := filepath.Join(dir, "cron", name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
}

func TestStoreConcurrentAppendsAreLineAtomic(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	at := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	const writers = 16
	const perWriter = 200

	line := strings.Repeat("x", 8000) // > pipe atomic threshold to stress lock
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := store.Append("cron", at, []byte(line)); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	path := filepath.Join(dir, "cron", "2026-04-24.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	count := 0
	for scanner.Scan() {
		if scanner.Text() != line {
			t.Fatalf("interleaved write at line %d", count)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != writers*perWriter {
		t.Fatalf("count = %d; want %d", count, writers*perWriter)
	}
}
