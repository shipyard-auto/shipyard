package cron

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryRepo struct {
	store Store
}

func (m *memoryRepo) Load() (Store, error) {
	return m.store, nil
}

func (m *memoryRepo) Save(store Store) error {
	m.store = store
	return nil
}

type fakeCrontab struct {
	readValue string
	written   string
	readErr   error
	writeErr  error
}

func (f *fakeCrontab) Read() (string, error) {
	return f.readValue, f.readErr
}

func (f *fakeCrontab) Write(contents string) error {
	f.written = contents
	return f.writeErr
}

type fakeIDGen struct {
	id string
}

func (f fakeIDGen) NewID(_ map[string]struct{}) (string, error) {
	return f.id, nil
}

func TestServiceAddPersistsAndSyncsCrontab(t *testing.T) {
	t.Parallel()

	repo := &memoryRepo{store: Store{Version: storeVersion, Jobs: []Job{}}}
	crontab := &fakeCrontab{readValue: "MAILTO=user@example.com\n"}
	now := time.Date(2026, time.April, 14, 12, 0, 0, 0, time.UTC)

	service := Service{
		Repo:    repo,
		Crontab: crontab,
		IDGen:   fakeIDGen{id: "AB12CD"},
		Now:     func() time.Time { return now },
	}

	job, err := service.Add(JobInput{
		Name:     strptr("Backup"),
		Schedule: strptr("0 * * * *"),
		Command:  strptr("/usr/local/bin/backup"),
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if job.ID != "AB12CD" {
		t.Fatalf("job.ID = %q, want %q", job.ID, "AB12CD")
	}
	if len(repo.store.Jobs) != 1 {
		t.Fatalf("len(repo.store.Jobs) = %d, want 1", len(repo.store.Jobs))
	}
	if crontab.written == "" || !containsAll(crontab.written, "MAILTO=user@example.com", "# shipyard:AB12CD Backup", "0 * * * * /usr/local/bin/backup") {
		t.Fatalf("crontab sync missing expected content: %q", crontab.written)
	}
}

func TestServiceUpdateDisablesJobInCrontab(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 14, 12, 0, 0, 0, time.UTC)
	repo := &memoryRepo{store: Store{
		Version: storeVersion,
		Jobs: []Job{{
			ID:        "AB12CD",
			Name:      "Backup",
			Schedule:  "0 * * * *",
			Command:   "/usr/local/bin/backup",
			Enabled:   true,
			CreatedAt: now,
			UpdatedAt: now,
		}},
	}}
	crontab := &fakeCrontab{readValue: "# shipyard:AB12CD Backup\n0 * * * * /usr/local/bin/backup\n"}

	service := Service{
		Repo:    repo,
		Crontab: crontab,
		IDGen:   fakeIDGen{id: "AB12CD"},
		Now:     func() time.Time { return now.Add(time.Hour) },
	}

	updated, err := service.Update("AB12CD", JobInput{Enabled: boolptr(false)})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if updated.Enabled {
		t.Fatal("updated.Enabled = true, want false")
	}
	if crontab.written != "" {
		t.Fatalf("crontab.written = %q, want empty crontab after disabling job", crontab.written)
	}
}

func TestServiceDeleteRemovesJob(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	repo := &memoryRepo{store: Store{
		Version: storeVersion,
		Jobs: []Job{{
			ID:        "AB12CD",
			Name:      "Backup",
			Schedule:  "0 * * * *",
			Command:   "/usr/local/bin/backup",
			Enabled:   true,
			CreatedAt: now,
			UpdatedAt: now,
		}},
	}}
	crontab := &fakeCrontab{readValue: "# shipyard:AB12CD Backup\n0 * * * * /usr/local/bin/backup\n"}

	service := Service{Repo: repo, Crontab: crontab, IDGen: fakeIDGen{id: "ZZ99ZZ"}, Now: time.Now}

	if err := service.Delete("AB12CD"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(repo.store.Jobs) != 0 {
		t.Fatalf("len(repo.store.Jobs) = %d, want 0", len(repo.store.Jobs))
	}
}

func TestServiceReturnsNotFound(t *testing.T) {
	t.Parallel()

	service := Service{
		Repo:    &memoryRepo{store: Store{Version: storeVersion, Jobs: []Job{}}},
		Crontab: &fakeCrontab{},
		IDGen:   fakeIDGen{id: "AA11BB"},
		Now:     time.Now,
	}

	_, err := service.Get("MISSING")
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("Get() error = %v, want ErrJobNotFound", err)
	}
}

func strptr(value string) *string { return &value }
func boolptr(value bool) *bool    { return &value }

func containsAll(text string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(text, value) {
			return false
		}
	}
	return true
}
