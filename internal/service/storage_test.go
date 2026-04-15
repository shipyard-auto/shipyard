package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileRepositoryLoadMissingReturnsEmptyStore(t *testing.T) {
	repo := FileRepository{Path: filepath.Join(t.TempDir(), "services.json")}
	store, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if store.Version != storeVersion || store.Notice != storeNotice {
		t.Fatalf("unexpected store metadata: %+v", store)
	}
	if len(store.Services) != 0 {
		t.Fatalf("expected empty services, got %+v", store.Services)
	}
}

func TestFileRepositoryRoundTrip(t *testing.T) {
	repo := FileRepository{Path: filepath.Join(t.TempDir(), "services.json")}
	now := time.Now().UTC().Round(0)
	store := Store{
		Services: []ServiceRecord{{
			ID: "AB12CD", Name: "Heartbeat", Command: "/bin/echo ok", Enabled: true, CreatedAt: now, UpdatedAt: now,
		}},
	}
	if err := repo.Save(store); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Services) != 1 || loaded.Services[0].ID != "AB12CD" {
		t.Fatalf("unexpected loaded store: %+v", loaded)
	}
	data, err := os.ReadFile(repo.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("expected trailing newline")
	}
}

