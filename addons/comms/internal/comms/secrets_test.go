package comms

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const secretChannelID = "01htgkx7r4qx9z8m6n3v2bp4ay"

func TestSecretStore_Write_FilePerm0600(t *testing.T) {
	tmp := t.TempDir()
	store := NewSecretStore(tmp)

	if err := store.Write(secretChannelID, []byte(`{"token":"x"}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(store.Path(secretChannelID))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 0600", perm)
	}
}

func TestSecretStore_Write_DirPerm0700(t *testing.T) {
	// Force directory creation by giving a nested non-existent home.
	tmp := filepath.Join(t.TempDir(), "comms-home")
	store := NewSecretStore(tmp)

	if err := store.Write(secretChannelID, []byte(`{}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(SecretsDir(tmp))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 0700", perm)
	}
}

func TestSecretStore_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	store := NewSecretStore(tmp)

	want := []byte(`{"token":"abc","extra":42}`)
	if err := store.Write(secretChannelID, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := store.Read(secretChannelID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("round-trip mismatch\nwant=%q\ngot =%q", want, got)
	}
}

func TestSecretStore_Read_MissingReturnsNotExist(t *testing.T) {
	tmp := t.TempDir()
	store := NewSecretStore(tmp)

	_, err := store.Read(secretChannelID)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected wrap of os.ErrNotExist, got %v", err)
	}
}

func TestSecretStore_Delete_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	store := NewSecretStore(tmp)

	// Delete before anything exists must be a no-op.
	if err := store.Delete(secretChannelID); err != nil {
		t.Errorf("first Delete (no file): %v", err)
	}
	if err := store.Write(secretChannelID, []byte(`{}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := store.Delete(secretChannelID); err != nil {
		t.Errorf("Delete (existing): %v", err)
	}
	if err := store.Delete(secretChannelID); err != nil {
		t.Errorf("Delete (already deleted): %v", err)
	}
}

func TestSecretStore_Write_RejectsInvalidChannelID(t *testing.T) {
	tmp := t.TempDir()
	store := NewSecretStore(tmp)

	err := store.Write("BAD", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Ensure nothing got written and the secrets dir wasn't even created.
	if _, statErr := os.Stat(SecretsDir(tmp)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("secrets dir must not exist after rejected Write, stat err=%v", statErr)
	}
}

func TestSecretStore_Read_RejectsInvalidChannelID(t *testing.T) {
	tmp := t.TempDir()
	store := NewSecretStore(tmp)
	_, err := store.Read("BAD")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSecretStore_Delete_RejectsInvalidChannelID(t *testing.T) {
	tmp := t.TempDir()
	store := NewSecretStore(tmp)
	err := store.Delete("BAD")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSecretStore_Write_OverwritesExisting(t *testing.T) {
	tmp := t.TempDir()
	store := NewSecretStore(tmp)

	if err := store.Write(secretChannelID, []byte(`{"token":"A"}`)); err != nil {
		t.Fatalf("Write A: %v", err)
	}
	if err := store.Write(secretChannelID, []byte(`{"token":"B"}`)); err != nil {
		t.Fatalf("Write B: %v", err)
	}
	got, err := store.Read(secretChannelID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != `{"token":"B"}` {
		t.Errorf("expected B to overwrite A, got %q", got)
	}
}
