package comms

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// isolateHome redirects $HOME and clears SHIPYARD_HOME so CommsHome()
// falls back to a per-test tmpdir. Same shape as helpers used elsewhere
// in the repo (internal/cli/uninstall_test.go::isolateHome).
func isolateHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("SHIPYARD_HOME", "")
	return tmp
}

func twoValidChannels() []Channel {
	ts := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	return []Channel{
		{
			ID:        "01htgkx7r4qx9z8m6n3v2bp4ay",
			Name:      "alpha",
			Type:      ChannelTypeTelegram,
			CreatedAt: ts,
			UpdatedAt: ts,
		},
		{
			ID:        "02htgkx7r4qx9z8m6n3v2bp4ay",
			Name:      "beta",
			Type:      ChannelTypeTelegram,
			Settings:  map[string]any{"bot_username": "betabot"},
			CreatedAt: ts,
			UpdatedAt: ts,
		},
	}
}

func TestCommsHome_HonorsEnvVar(t *testing.T) {
	t.Setenv("SHIPYARD_HOME", "/tmp/custom-shipyard")
	got, err := CommsHome()
	if err != nil {
		t.Fatalf("CommsHome: %v", err)
	}
	want := "/tmp/custom-shipyard/comms"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCommsHome_FallsBackToHome(t *testing.T) {
	home := isolateHome(t)
	got, err := CommsHome()
	if err != nil {
		t.Fatalf("CommsHome: %v", err)
	}
	want := filepath.Join(home, ".shipyard", "comms")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestChannelStore_LoadMissing_EmptyNoError(t *testing.T) {
	tmp := t.TempDir()
	store := NewChannelStore(tmp)
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestChannelStore_SaveLoad_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	store := NewChannelStore(tmp)

	want := twoValidChannels()
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round-trip mismatch\nwant=%+v\ngot =%+v", want, got)
	}
}

func TestChannelStore_Save_RejectsInvalidChannel(t *testing.T) {
	tmp := t.TempDir()
	store := NewChannelStore(tmp)

	bad := twoValidChannels()
	bad[0].Name = "1bot" // starts with digit, violates channelNamePattern

	if err := store.Save(bad); err == nil {
		t.Fatal("expected error, got nil")
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file must NOT exist after rejected Save, stat err=%v", err)
	}
}

func TestChannelStore_Save_RejectsDuplicateName(t *testing.T) {
	tmp := t.TempDir()
	store := NewChannelStore(tmp)

	dup := twoValidChannels()
	dup[1].Name = "Alpha" // case-insensitive collision with "alpha"

	err := store.Save(dup)
	if err == nil {
		t.Fatal("expected duplicate-name error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate channel name") {
		t.Errorf("unexpected error message: %v", err)
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file must NOT exist after rejected Save, stat err=%v", err)
	}
}

func TestChannelStore_Save_NoLeftoverTempFiles(t *testing.T) {
	tmp := t.TempDir()
	store := NewChannelStore(tmp)

	if err := store.Save(twoValidChannels()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after successful Save: %s", e.Name())
		}
	}
}

func TestChannelStore_Save_CreatesDirWithPerm0700(t *testing.T) {
	// Use a tmpdir + nested non-existent subdirectory to force creation.
	tmp := filepath.Join(t.TempDir(), "comms-home")
	store := NewChannelStore(tmp)
	if err := store.Save(twoValidChannels()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(tmp)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 0700", perm)
	}
}

func TestChannelStore_Save_FileIsPerm0644(t *testing.T) {
	tmp := t.TempDir()
	store := NewChannelStore(tmp)
	if err := store.Save(twoValidChannels()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("file perm = %o, want 0644", perm)
	}
}

func TestChannelStore_Load_RejectsMalformedJSON(t *testing.T) {
	tmp := t.TempDir()
	store := NewChannelStore(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(store.Path(), []byte("{garbage"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := store.Load()
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("expected error mentioning parse, got %v", err)
	}
}

func TestChannelStore_Load_RejectsInvalidChannelOnDisk(t *testing.T) {
	tmp := t.TempDir()
	store := NewChannelStore(tmp)
	// Hand-craft a JSON file that would fail Validate (empty name).
	bad := `{"channels":[{"id":"01htgkx7r4qx9z8m6n3v2bp4ay","name":"","type":"telegram","created_at":"2026-05-24T12:00:00Z","updated_at":"2026-05-24T12:00:00Z"}]}`
	if err := os.WriteFile(store.Path(), []byte(bad), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := store.Load()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") || !strings.Contains(err.Error(), "name") {
		t.Errorf("error should mention invalid name, got %v", err)
	}
}

func TestChannelStore_Save_EmptySliceWritesEmptyArray(t *testing.T) {
	tmp := t.TempDir()
	store := NewChannelStore(tmp)
	if err := store.Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), `"channels": []`) {
		t.Errorf("expected explicit empty array in JSON, got: %s", raw)
	}
}
