package cron

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

// recordingHandler captures slog records emitted by a Service under test so
// assertions can inspect emitted attributes (e.g. output_tail).
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *recordingHandler) find(event string) (slog.Record, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message == event {
			return r, true
		}
	}
	return slog.Record{}, false
}

func attrString(r slog.Record, key string) (string, bool) {
	var out string
	var ok bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			out = a.Value.String()
			ok = true
			return false
		}
		return true
	})
	return out, ok
}

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
		Repo:       repo,
		Crontab:    crontab,
		IDGen:      fakeIDGen{id: "AB12CD"},
		Now:        func() time.Time { return now },
		BinaryPath: "/usr/local/bin/shipyard",
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
	if crontab.written == "" || !containsAll(crontab.written, "MAILTO=user@example.com", "# shipyard:AB12CD Backup", "0 * * * * /usr/local/bin/shipyard cron run AB12CD") {
		t.Fatalf("crontab sync missing expected content: %q", crontab.written)
	}
	if strings.Contains(crontab.written, "/usr/local/bin/backup") {
		t.Fatalf("crontab leaked raw user command: %q", crontab.written)
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
		Repo:       repo,
		Crontab:    crontab,
		IDGen:      fakeIDGen{id: "AB12CD"},
		Now:        func() time.Time { return now.Add(time.Hour) },
		BinaryPath: "/usr/local/bin/shipyard",
	}

	updated, err := service.Update("AB12CD", JobInput{Enabled: boolptrTest(false)})
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

	service := Service{Repo: repo, Crontab: crontab, IDGen: fakeIDGen{id: "ZZ99ZZ"}, Now: time.Now, BinaryPath: "/usr/local/bin/shipyard"}

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

func TestServiceAddRejectsInvalidSchedule(t *testing.T) {
	t.Parallel()

	service := Service{
		Repo:    &memoryRepo{store: Store{Notice: storeNotice, Version: storeVersion, Jobs: []Job{}}},
		Crontab: &fakeCrontab{},
		IDGen:   fakeIDGen{id: "AA11BB"},
		Now:     time.Now,
	}

	_, err := service.Add(JobInput{
		Name:     strptr("Broken"),
		Schedule: strptr("* *"),
		Command:  strptr("/bin/echo hello"),
	})
	if err == nil || !strings.Contains(err.Error(), "schedule") {
		t.Fatalf("Add() error = %v, want schedule validation error", err)
	}
}

func TestServiceUpdateRejectsInvalidSchedule(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	service := Service{
		Repo: &memoryRepo{store: Store{
			Notice:  storeNotice,
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
		}},
		Crontab: &fakeCrontab{readValue: "# shipyard:AB12CD Backup\n0 * * * * /usr/local/bin/backup\n"},
		IDGen:   fakeIDGen{id: "AA11BB"},
		Now:     time.Now,
	}

	_, err := service.Update("AB12CD", JobInput{Schedule: strptr("99 * * * *")})
	if err == nil || !strings.Contains(err.Error(), "schedule") {
		t.Fatalf("Update() error = %v, want schedule validation error", err)
	}
}

func TestServiceAddUsesResolveBinaryFallback(t *testing.T) {
	t.Parallel()

	repo := &memoryRepo{store: Store{Version: storeVersion, Jobs: []Job{}}}
	crontab := &fakeCrontab{}
	now := time.Date(2026, time.April, 14, 12, 0, 0, 0, time.UTC)

	service := Service{
		Repo:          repo,
		Crontab:       crontab,
		IDGen:         fakeIDGen{id: "AB12CD"},
		Now:           func() time.Time { return now },
		ResolveBinary: func() (string, error) { return "/opt/shipyard/bin/shipyard", nil },
	}

	if _, err := service.Add(JobInput{
		Name:     strptr("Backup"),
		Schedule: strptr("0 * * * *"),
		Command:  strptr("/usr/local/bin/backup"),
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if !strings.Contains(crontab.written, "0 * * * * /opt/shipyard/bin/shipyard cron run AB12CD") {
		t.Fatalf("crontab missing wrapper from ResolveBinary fallback: %q", crontab.written)
	}
}

func TestServiceAddFailsWhenBinaryUnresolvable(t *testing.T) {
	t.Parallel()

	repo := &memoryRepo{store: Store{Version: storeVersion, Jobs: []Job{}}}
	crontab := &fakeCrontab{}
	now := time.Date(2026, time.April, 14, 12, 0, 0, 0, time.UTC)

	service := Service{
		Repo:          repo,
		Crontab:       crontab,
		IDGen:         fakeIDGen{id: "AB12CD"},
		Now:           func() time.Time { return now },
		ResolveBinary: func() (string, error) { return "", errors.New("boom") },
	}

	_, err := service.Add(JobInput{
		Name:     strptr("Backup"),
		Schedule: strptr("0 * * * *"),
		Command:  strptr("/usr/local/bin/backup"),
	})
	if err == nil || !strings.Contains(err.Error(), "resolve shipyard binary") {
		t.Fatalf("Add() error = %v, want resolve shipyard binary error", err)
	}
	if crontab.written != "" {
		t.Fatalf("crontab.written = %q, want empty when binary resolution fails", crontab.written)
	}
}

func TestServiceRunExecutesCommand(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	service := Service{
		Repo: &memoryRepo{store: Store{
			Notice:  storeNotice,
			Version: storeVersion,
			Jobs: []Job{{
				ID:        "AB12CD",
				Name:      "Backup",
				Schedule:  "0 * * * *",
				Command:   "echo hello",
				Enabled:   true,
				CreatedAt: now,
				UpdatedAt: now,
			}},
		}},
		Crontab: &fakeCrontab{},
		IDGen:   fakeIDGen{id: "AA11BB"},
		Now:     time.Now,
		Exec: func(name string, args ...string) *exec.Cmd {
			return exec.Command("sh", "-lc", "echo hello")
		},
	}

	job, output, err := service.Run(context.Background(), "AB12CD")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if job.ID != "AB12CD" {
		t.Fatalf("job.ID = %q, want %q", job.ID, "AB12CD")
	}
	if strings.TrimSpace(output) != "hello" {
		t.Fatalf("output = %q, want %q", output, "hello")
	}
}

// TestServiceRun_emitsOutputTail_withFinalBytes asserts that the cron
// runner emits output_tail carrying the *last* DefaultOutputTailBytes of the
// subprocess stdout. This is the canary for the C-02 generalization: errors
// usually surface at the end of process output, so the tail is what an
// operator wants to see post-mortem.
func TestServiceRun_emitsOutputTail_withFinalBytes(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rec := &recordingHandler{}

	// Build a script whose total stdout > DefaultOutputTailBytes and whose
	// distinctive marker is at the very end. The tail must contain MARKER_END,
	// the head field (output) might or might not.
	const marker = "MARKER_END_OF_OUTPUT"
	padding := strings.Repeat("a", yardlogs.DefaultOutputTailBytes*2)
	script := "printf '%s%s' '" + padding + "' '" + marker + "'"

	service := Service{
		Repo: &memoryRepo{store: Store{
			Notice:  storeNotice,
			Version: storeVersion,
			Jobs: []Job{{
				ID:        "AB12CD",
				Name:      "Loud",
				Schedule:  "0 * * * *",
				Command:   script,
				Enabled:   true,
				CreatedAt: now,
				UpdatedAt: now,
			}},
		}},
		Crontab: &fakeCrontab{},
		IDGen:   fakeIDGen{id: "AA11BB"},
		Now:     time.Now,
		Exec: func(name string, args ...string) *exec.Cmd {
			return exec.Command("sh", "-lc", script)
		},
		Logger: slog.New(rec),
	}

	if _, _, err := service.Run(context.Background(), "AB12CD"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	r, ok := rec.find(yardlogs.EventCronJobRunFinished)
	if !ok {
		t.Fatal("expected cron_job_run_finished record; got none")
	}
	tail, ok := attrString(r, yardlogs.KeyOutputTail)
	if !ok {
		t.Fatal("output_tail attr missing on cron_job_run_finished")
	}
	if !strings.HasSuffix(tail, marker) {
		t.Fatalf("output_tail does not end with marker; tail tail = %q", tail[max(0, len(tail)-len(marker)-10):])
	}
	if len(tail) > yardlogs.DefaultOutputTailBytes {
		t.Fatalf("output_tail = %d bytes, want ≤ %d", len(tail), yardlogs.DefaultOutputTailBytes)
	}
}

func strptr(value string) *string  { return &value }
func boolptrTest(value bool) *bool { return &value }

func containsAll(text string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(text, value) {
			return false
		}
	}
	return true
}
