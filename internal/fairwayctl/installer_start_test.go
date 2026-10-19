package fairwayctl

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/internal/service"
)

// newUpgradeInstaller returns an Installer at version "0.23" with an older
// "0.22" binary already at BinPath, so Upgrade has something to replace.
func newUpgradeInstaller(t *testing.T, sa ServiceAdder) *Installer {
	t.Helper()
	client, _ := buildInstallHTTPClient(t, "0.23", Platform{OS: "linux", Arch: "amd64"})
	inst := newTestInstaller(t, client, sa)
	inst.Version = "0.23"
	makeFakeShellScript(t, inst.BinDir, BinaryName, "shipyard-fairway 0.22 (abc, built now)")
	return inst
}

func TestInstall_startsDaemon(t *testing.T) {
	client, _ := buildInstallHTTPClient(t, "0.22", Platform{OS: "linux", Arch: "amd64"})
	sa := &fakeServiceAdder{}
	inst := newTestInstaller(t, client, sa)

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if want := []string{"add", "start"}; !slices.Equal(sa.calls, want) {
		t.Errorf("calls = %v, want %v", sa.calls, want)
	}
	if !sa.running {
		t.Error("daemon not running after Install")
	}
}

func TestInstall_startFails_keepsInstallAndReturnsErrStartFailed(t *testing.T) {
	client, _ := buildInstallHTTPClient(t, "0.22", Platform{OS: "linux", Arch: "amd64"})
	sa := &fakeServiceAdder{startErr: errors.New("start service AB12CD (run 'shipyard service start AB12CD'): boom")}
	inst := newTestInstaller(t, client, sa)

	err := inst.Install(context.Background())
	if !errors.Is(err, ErrStartFailed) {
		t.Fatalf("err = %v, want ErrStartFailed", err)
	}
	if !strings.Contains(err.Error(), "shipyard service start AB12CD") {
		t.Errorf("error does not carry the start command: %v", err)
	}
	if _, statErr := os.Stat(inst.BinPath()); statErr != nil {
		t.Errorf("binary removed after start failure: %v", statErr)
	}
}

func TestUpgrade_wasRunning_startsNewDaemon(t *testing.T) {
	sa := &fakeServiceAdder{running: true}
	inst := newUpgradeInstaller(t, sa)

	if err := inst.Upgrade(context.Background()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if want := []string{"status", "remove", "add", "start"}; !slices.Equal(sa.calls, want) {
		t.Errorf("calls = %v, want %v", sa.calls, want)
	}
	if !sa.running {
		t.Error("daemon left down after Upgrade")
	}
}

func TestUpgrade_wasStopped_leavesItStopped(t *testing.T) {
	sa := &fakeServiceAdder{running: false}
	inst := newUpgradeInstaller(t, sa)

	if err := inst.Upgrade(context.Background()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if sa.started {
		t.Error("Upgrade started a daemon the user had stopped")
	}
	if !sa.added {
		t.Error("service not re-registered")
	}
}

func TestUpgrade_statusUnknown_startsDaemon(t *testing.T) {
	sa := &fakeServiceAdder{runningErr: errors.New("systemctl: bus unavailable")}
	inst := newUpgradeInstaller(t, sa)

	if err := inst.Upgrade(context.Background()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if !sa.started {
		t.Error("Upgrade must start the daemon when its previous state is unknown")
	}
}

func TestUpgrade_startFails_returnsErrStartFailed(t *testing.T) {
	sa := &fakeServiceAdder{running: true, startErr: errors.New("boom")}
	inst := newUpgradeInstaller(t, sa)

	err := inst.Upgrade(context.Background())
	if !errors.Is(err, ErrStartFailed) {
		t.Fatalf("err = %v, want ErrStartFailed", err)
	}
	if strings.Contains(err.Error(), "shipyard fairway install") {
		t.Errorf("start failure must not suggest reinstalling: %v", err)
	}
}

// ── Real serviceAdderImpl over a fake service.Manager ────────────────────────

type memRepo struct{ store service.Store }

func (r *memRepo) Load() (service.Store, error) { return r.store, nil }
func (r *memRepo) Save(s service.Store) error {
	r.store = s
	return nil
}

type seqIDGen struct{ n int }

func (g *seqIDGen) NewID(map[string]struct{}) (string, error) {
	g.n++
	return "SVC00" + string(rune('0'+g.n)), nil
}

// fakeManager mimics systemd: Enable never starts a unit, Remove stops it.
type fakeManager struct {
	running map[string]bool
	started []string
}

func (m *fakeManager) Platform() service.Platform         { return service.Platform("linux") }
func (m *fakeManager) Sync([]service.ServiceRecord) error { return nil }
func (m *fakeManager) Reload() error                      { return nil }
func (m *fakeManager) Stop(id string) error               { delete(m.running, id); return nil }
func (m *fakeManager) Restart(id string) error            { return m.Start(id) }
func (m *fakeManager) Enable(string) error                { return nil }
func (m *fakeManager) Disable(string) error               { return nil }
func (m *fakeManager) Remove(id string) error             { delete(m.running, id); return nil }
func (m *fakeManager) Start(id string) error {
	m.started = append(m.started, id)
	m.running[id] = true
	return nil
}
func (m *fakeManager) Status(id string) (service.RuntimeStatus, error) {
	if m.running[id] {
		return service.RuntimeStatus{State: "active", SubState: "running", PID: 42}, nil
	}
	return service.RuntimeStatus{State: "inactive", SubState: "dead"}, nil
}

func newFakeServiceAdder(t *testing.T) (*serviceAdderImpl, *fakeManager) {
	t.Helper()
	mgr := &fakeManager{running: map[string]bool{}}
	return &serviceAdderImpl{svc: service.Service{
		Repo:    &memRepo{store: service.Store{Version: 1}},
		Manager: mgr,
		IDGen:   &seqIDGen{},
		Now:     time.Now,
	}}, mgr
}

// TestUpgrade_realServiceAdder_daemonRunningAfterwards reproduces the
// 2026-10-17 outage: the upgrade deleted the running service and re-added
// it enabled but stopped. The new service must end up running.
func TestUpgrade_realServiceAdder_daemonRunningAfterwards(t *testing.T) {
	sa, mgr := newFakeServiceAdder(t)
	inst := newUpgradeInstaller(t, sa)

	if err := sa.AddFairway(inst.BinPath()); err != nil {
		t.Fatal(err)
	}
	if err := sa.StartFairway(); err != nil {
		t.Fatal(err)
	}
	mgr.started = nil

	if err := inst.Upgrade(context.Background()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	r, ok, err := sa.find()
	if err != nil || !ok {
		t.Fatalf("fairway service missing after upgrade: ok=%v err=%v", ok, err)
	}
	if !slices.Equal(mgr.started, []string{r.ID}) {
		t.Errorf("started = %v, want [%s]", mgr.started, r.ID)
	}
	if running, _ := sa.IsFairwayRunning(); !running {
		t.Error("fairway daemon down after Upgrade")
	}
}

func TestServiceAdder_isFairwayRunning_noService(t *testing.T) {
	sa, _ := newFakeServiceAdder(t)
	running, err := sa.IsFairwayRunning()
	if err != nil || running {
		t.Errorf("IsFairwayRunning() = %v, %v; want false, nil", running, err)
	}
}

func TestServiceAdder_startFairway_noService(t *testing.T) {
	sa, _ := newFakeServiceAdder(t)
	if err := sa.StartFairway(); err == nil {
		t.Error("StartFairway with no registered service must fail")
	}
}
