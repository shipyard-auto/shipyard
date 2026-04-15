package service

import (
	"errors"
	"testing"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

type memoryRepo struct {
	store   Store
	loadErr error
	saveErr error
}

func (r *memoryRepo) Load() (Store, error) {
	if r.loadErr != nil {
		return Store{}, r.loadErr
	}
	if r.store.Services == nil {
		r.store = Store{Notice: storeNotice, Version: storeVersion, Services: []ServiceRecord{}}
	}
	return r.store, nil
}

func (r *memoryRepo) Save(store Store) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.store = store
	return nil
}

type fakeManager struct {
	syncErr    error
	reloadErr  error
	enableErr  error
	disableErr error
	startErr   error
	stopErr    error
	restartErr error
	removeErr  error
	statuses   map[string]RuntimeStatus
	calls      []string
}

func (m *fakeManager) Platform() Platform { return PlatformSystemd }
func (m *fakeManager) Sync(desired []ServiceRecord) error {
	m.calls = append(m.calls, "sync")
	return m.syncErr
}
func (m *fakeManager) Reload() error { m.calls = append(m.calls, "reload"); return m.reloadErr }
func (m *fakeManager) Start(id string) error { m.calls = append(m.calls, "start:"+id); return m.startErr }
func (m *fakeManager) Stop(id string) error { m.calls = append(m.calls, "stop:"+id); return m.stopErr }
func (m *fakeManager) Restart(id string) error { m.calls = append(m.calls, "restart:"+id); return m.restartErr }
func (m *fakeManager) Status(id string) (RuntimeStatus, error) {
	m.calls = append(m.calls, "status:"+id)
	return m.statuses[id], nil
}
func (m *fakeManager) Enable(id string) error { m.calls = append(m.calls, "enable:"+id); return m.enableErr }
func (m *fakeManager) Disable(id string) error { m.calls = append(m.calls, "disable:"+id); return m.disableErr }
func (m *fakeManager) Remove(id string) error { m.calls = append(m.calls, "remove:"+id); return m.removeErr }

type fakeIDGen struct {
	id  string
	err error
}

func (g fakeIDGen) NewID(existing map[string]struct{}) (string, error) {
	if g.err != nil {
		return "", g.err
	}
	return g.id, nil
}

type fakeLogger struct {
	events []yardlogs.Event
}

func (l *fakeLogger) Write(event yardlogs.Event) error {
	l.events = append(l.events, event)
	return nil
}

func TestServiceAdd(t *testing.T) {
	repo := &memoryRepo{store: Store{Notice: storeNotice, Version: storeVersion, Services: []ServiceRecord{}}}
	manager := &fakeManager{}
	logger := &fakeLogger{}
	service := Service{Repo: repo, Manager: manager, IDGen: fakeIDGen{id: "AB12CD"}, Now: time.Now, Logger: logger}
	name, cmd := "Heartbeat", "/bin/echo ok"
	record, err := service.Add(ServiceInput{Name: &name, Command: &cmd})
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != "AB12CD" || len(repo.store.Services) != 1 {
		t.Fatalf("unexpected record/store: %+v %+v", record, repo.store)
	}
	if len(logger.events) == 0 || logger.events[0].Event != "service_created" {
		t.Fatalf("unexpected log events: %+v", logger.events)
	}
}

func TestServiceAddRollbackOnSyncFailure(t *testing.T) {
	repo := &memoryRepo{store: Store{Notice: storeNotice, Version: storeVersion, Services: []ServiceRecord{}}}
	manager := &fakeManager{syncErr: errors.New("boom")}
	logger := &fakeLogger{}
	service := Service{Repo: repo, Manager: manager, IDGen: fakeIDGen{id: "AB12CD"}, Now: time.Now, Logger: logger}
	name, cmd := "Heartbeat", "/bin/echo ok"
	if _, err := service.Add(ServiceInput{Name: &name, Command: &cmd}); err == nil {
		t.Fatal("expected sync error")
	}
	if len(repo.store.Services) != 0 {
		t.Fatalf("expected rollback, got %+v", repo.store.Services)
	}
	if len(logger.events) == 0 || logger.events[0].Event != "service_create_failed" {
		t.Fatalf("unexpected log events: %+v", logger.events)
	}
}

func TestServiceUpdateOnlyNonNilFields(t *testing.T) {
	now := time.Now().UTC()
	repo := &memoryRepo{store: Store{Notice: storeNotice, Version: storeVersion, Services: []ServiceRecord{{
		ID: "AB12CD", Name: "Heartbeat", Command: "/bin/echo ok", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}}}}
	service := Service{Repo: repo, Manager: &fakeManager{}, IDGen: fakeIDGen{id: "ZZ99ZZ"}, Now: time.Now, Logger: &fakeLogger{}}
	name := "Renamed"
	record, err := service.Update("AB12CD", ServiceInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if record.Name != "Renamed" || record.Command != "/bin/echo ok" {
		t.Fatalf("unexpected updated record: %+v", record)
	}
}

func TestServiceDelete(t *testing.T) {
	now := time.Now().UTC()
	repo := &memoryRepo{store: Store{Notice: storeNotice, Version: storeVersion, Services: []ServiceRecord{{
		ID: "AB12CD", Name: "Heartbeat", Command: "/bin/echo ok", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}}}}
	manager := &fakeManager{}
	service := Service{Repo: repo, Manager: manager, IDGen: fakeIDGen{id: "ZZ99ZZ"}, Now: time.Now, Logger: &fakeLogger{}}
	if err := service.Delete("AB12CD"); err != nil {
		t.Fatal(err)
	}
	if len(repo.store.Services) != 0 {
		t.Fatalf("expected deleted service, got %+v", repo.store.Services)
	}
	if manager.calls[len(manager.calls)-1] != "remove:AB12CD" {
		t.Fatalf("expected remove call, got %+v", manager.calls)
	}
}

func TestServiceLifecycleAndStatus(t *testing.T) {
	now := time.Now().UTC()
	repo := &memoryRepo{store: Store{Notice: storeNotice, Version: storeVersion, Services: []ServiceRecord{{
		ID: "AB12CD", Name: "Heartbeat", Command: "/bin/echo ok", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}}}}
	manager := &fakeManager{statuses: map[string]RuntimeStatus{"AB12CD": {State: "active", PID: 42}}}
	service := Service{Repo: repo, Manager: manager, IDGen: fakeIDGen{id: "ZZ99ZZ"}, Now: time.Now, Logger: &fakeLogger{}}
	if _, err := service.Start("AB12CD"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Stop("AB12CD"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Restart("AB12CD"); err != nil {
		t.Fatal(err)
	}
	record, status, err := service.Status("AB12CD")
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != "AB12CD" || status.PID != 42 {
		t.Fatalf("unexpected status result: %+v %+v", record, status)
	}
}

func TestServiceGetMissing(t *testing.T) {
	service := Service{Repo: &memoryRepo{store: Store{Notice: storeNotice, Version: storeVersion, Services: []ServiceRecord{}}}}
	if _, err := service.Get("MISSING"); !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("expected ErrServiceNotFound, got %v", err)
	}
}
