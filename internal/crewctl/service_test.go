package crewctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls   [][]string
	results map[string]error // key = first arg sequence joined by "|"
	outputs map[string][]byte
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{results: map[string]error{}, outputs: map[string][]byte{}}
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), "|")
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.outputs[key], f.results[key]
}

func newManager(t *testing.T, platform string) (*Manager, *fakeRunner, string, string) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "bin", "shipyard-crew")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	r := newFakeRunner()
	m := &Manager{HomeDir: home, Platform: platform, Runner: r, UID: 501}
	return m, r, home, bin
}

func TestValidateName(t *testing.T) {
	ok := []string{"a", "0", "promo", "promo-hunter", "promo_hunter", "x1"}
	bad := []string{"", "A", "-bad", "_bad", "bad!", "bad/name", "bad name"}
	for _, s := range ok {
		if err := ValidateName(s); err != nil {
			t.Errorf("want %q valid, got %v", s, err)
		}
	}
	for _, s := range bad {
		if err := ValidateName(s); err == nil {
			t.Errorf("want %q invalid", s)
		}
	}
}

func TestPathsForUnsupportedPlatform(t *testing.T) {
	m := &Manager{HomeDir: "/h", Platform: "windows", Runner: newFakeRunner()}
	_, err := m.PathsFor("x")
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("err = %v", err)
	}
}

func TestPathsForDarwinAndLinux(t *testing.T) {
	for _, plat := range []string{PlatformDarwin, PlatformLinux} {
		m := &Manager{HomeDir: "/h", Platform: plat, Runner: newFakeRunner()}
		p, err := m.PathsFor("promo")
		if err != nil {
			t.Fatalf("paths %s: %v", plat, err)
		}
		if !strings.Contains(p.UnitFile, "promo") {
			t.Errorf("%s: unit %q missing name", plat, p.UnitFile)
		}
		if p.Label == "" {
			t.Errorf("%s: empty label", plat)
		}
	}
}

func TestRegisterRejectsInvalidName(t *testing.T) {
	m, _, _, bin := newManager(t, PlatformLinux)
	if err := m.RegisterAgentService(context.Background(), "BAD!", bin); err == nil {
		t.Fatalf("expected error")
	}
}

func TestRegisterMissingBinary(t *testing.T) {
	m, _, home, _ := newManager(t, PlatformLinux)
	err := m.RegisterAgentService(context.Background(), "promo", filepath.Join(home, "no-such"))
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestRegisterBinaryIsDirectory(t *testing.T) {
	m, _, home, _ := newManager(t, PlatformLinux)
	dir := filepath.Join(home, "dir")
	_ = os.Mkdir(dir, 0o755)
	if err := m.RegisterAgentService(context.Background(), "promo", dir); err == nil {
		t.Fatalf("expected error")
	}
}

func TestRegisterBinaryNotExecutable(t *testing.T) {
	m, _, home, _ := newManager(t, PlatformLinux)
	bin := filepath.Join(home, "noexec")
	if err := os.WriteFile(bin, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := m.RegisterAgentService(context.Background(), "promo", bin); err == nil {
		t.Fatalf("expected error")
	}
}

func TestRegisterUnsupportedPlatform(t *testing.T) {
	m, _, _, bin := newManager(t, "freebsd")
	if err := m.RegisterAgentService(context.Background(), "promo", bin); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("err = %v", err)
	}
}

func TestRegisterDarwinWritesPlistAndCallsLaunchctl(t *testing.T) {
	m, runner, home, bin := newManager(t, PlatformDarwin)
	if err := m.RegisterAgentService(context.Background(), "promo", bin); err != nil {
		t.Fatalf("register: %v", err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.shipyard.crew.promo.plist")
	data, err := os.ReadFile(plist)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	got := string(data)
	for _, want := range []string{"com.shipyard.crew.promo", "--service", "--agent", "promo", bin, ".out.log", ".err.log", "<true/>"} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q", want)
		}
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1; calls=%v", len(runner.calls), runner.calls)
	}
	if runner.calls[0][0] != "launchctl" || runner.calls[0][1] != "bootstrap" {
		t.Errorf("calls[0] = %v", runner.calls[0])
	}
	info, _ := os.Stat(plist)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("plist perm = %o", info.Mode().Perm())
	}
}

func TestRegisterDarwinFallsBackToLoad(t *testing.T) {
	m, runner, _, bin := newManager(t, PlatformDarwin)
	plistPath, _ := m.PathsFor("promo")
	bootKey := strings.Join([]string{"launchctl", "bootstrap", "gui/501", plistPath.UnitFile}, "|")
	runner.results[bootKey] = errors.New("not allowed")
	if err := m.RegisterAgentService(context.Background(), "promo", bin); err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %v", runner.calls)
	}
	if runner.calls[1][1] != "load" {
		t.Errorf("calls[1] = %v, want load", runner.calls[1])
	}
}

func TestRegisterDarwinBothLoadCommandsFail(t *testing.T) {
	m, runner, _, bin := newManager(t, PlatformDarwin)
	plistPath, _ := m.PathsFor("promo")
	runner.results[strings.Join([]string{"launchctl", "bootstrap", "gui/501", plistPath.UnitFile}, "|")] = errors.New("nope")
	runner.results[strings.Join([]string{"launchctl", "load", plistPath.UnitFile}, "|")] = errors.New("also nope")
	if err := m.RegisterAgentService(context.Background(), "promo", bin); err == nil {
		t.Fatalf("expected error")
	}
}

func TestRegisterLinuxWritesUnitAndCallsSystemctl(t *testing.T) {
	m, runner, home, bin := newManager(t, PlatformLinux)
	if err := m.RegisterAgentService(context.Background(), "promo", bin); err != nil {
		t.Fatalf("register: %v", err)
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "shipyard-crew-promo.service")
	data, err := os.ReadFile(unit)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	got := string(data)
	for _, want := range []string{"Description=Shipyard crew agent (promo)", "ExecStart=" + bin + " --service --agent promo", "Restart=always", "RestartSec=2"} {
		if !strings.Contains(got, want) {
			t.Errorf("unit missing %q", want)
		}
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %v", runner.calls)
	}
	want := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", "shipyard-crew-promo.service"},
	}
	for i, w := range want {
		if strings.Join(runner.calls[i], "|") != strings.Join(w, "|") {
			t.Errorf("call[%d] = %v, want %v", i, runner.calls[i], w)
		}
	}
}

func TestRegisterLinuxDaemonReloadFails(t *testing.T) {
	m, runner, _, bin := newManager(t, PlatformLinux)
	runner.results["systemctl|--user|daemon-reload"] = errors.New("dbus")
	if err := m.RegisterAgentService(context.Background(), "promo", bin); err == nil {
		t.Fatalf("expected error")
	}
}

func TestRegisterLinuxEnableFails(t *testing.T) {
	m, runner, _, bin := newManager(t, PlatformLinux)
	runner.results["systemctl|--user|enable|--now|shipyard-crew-promo.service"] = errors.New("nope")
	if err := m.RegisterAgentService(context.Background(), "promo", bin); err == nil {
		t.Fatalf("expected error")
	}
}

func TestUnregisterDarwinRemovesPlist(t *testing.T) {
	m, runner, home, bin := newManager(t, PlatformDarwin)
	if err := m.RegisterAgentService(context.Background(), "promo", bin); err != nil {
		t.Fatalf("register: %v", err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.shipyard.crew.promo.plist")
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	runner.calls = nil
	if err := m.UnregisterAgentService(context.Background(), "promo"); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if _, err := os.Stat(plist); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("plist still present: %v", err)
	}
	if len(runner.calls) == 0 || runner.calls[0][0] != "launchctl" || runner.calls[0][1] != "bootout" {
		t.Errorf("calls = %v", runner.calls)
	}
}

func TestUnregisterDarwinBootoutFailsFallsBackToUnload(t *testing.T) {
	m, runner, _, _ := newManager(t, PlatformDarwin)
	paths, _ := m.PathsFor("promo")
	runner.results["launchctl|bootout|gui/501/"+paths.Label] = errors.New("not loaded")
	// File doesn't exist — Unregister should silence and call unload.
	if err := m.UnregisterAgentService(context.Background(), "promo"); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %v", runner.calls)
	}
	if runner.calls[1][1] != "unload" {
		t.Errorf("calls[1] = %v", runner.calls[1])
	}
}

func TestUnregisterLinuxRemovesUnit(t *testing.T) {
	m, runner, home, bin := newManager(t, PlatformLinux)
	if err := m.RegisterAgentService(context.Background(), "promo", bin); err != nil {
		t.Fatalf("register: %v", err)
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "shipyard-crew-promo.service")
	if _, err := os.Stat(unit); err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	runner.calls = nil
	if err := m.UnregisterAgentService(context.Background(), "promo"); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if _, err := os.Stat(unit); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unit still present: %v", err)
	}
	wantFirst := []string{"systemctl", "--user", "disable", "--now", "shipyard-crew-promo.service"}
	if strings.Join(runner.calls[0], "|") != strings.Join(wantFirst, "|") {
		t.Errorf("calls[0] = %v, want %v", runner.calls[0], wantFirst)
	}
	wantLast := []string{"systemctl", "--user", "daemon-reload"}
	if strings.Join(runner.calls[len(runner.calls)-1], "|") != strings.Join(wantLast, "|") {
		t.Errorf("last = %v, want %v", runner.calls[len(runner.calls)-1], wantLast)
	}
}

func TestUnregisterMissingFileIsIdempotent(t *testing.T) {
	m, _, _, _ := newManager(t, PlatformLinux)
	if err := m.UnregisterAgentService(context.Background(), "ghost"); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestUnregisterRejectsInvalidName(t *testing.T) {
	m, _, _, _ := newManager(t, PlatformLinux)
	if err := m.UnregisterAgentService(context.Background(), "BAD"); err == nil {
		t.Errorf("expected error")
	}
}

func TestUnregisterUnsupportedPlatform(t *testing.T) {
	m, _, _, _ := newManager(t, "windows")
	if err := m.UnregisterAgentService(context.Background(), "promo"); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("err = %v", err)
	}
}

// Golden file comparison — keep templates stable.
func TestRenderLaunchdPlistGolden(t *testing.T) {
	m := &Manager{HomeDir: "HOME", Platform: PlatformDarwin}
	paths, _ := m.PathsFor("promo")
	got := renderLaunchdPlist("promo", "/usr/local/bin/shipyard-crew", paths)
	want, err := os.ReadFile(filepath.Join("testdata", "launchd_promo.plist"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("plist mismatch:\n--got--\n%s\n--want--\n%s", got, string(want))
	}
}

func TestRenderSystemdUnitGolden(t *testing.T) {
	m := &Manager{HomeDir: "HOME", Platform: PlatformLinux}
	paths, _ := m.PathsFor("promo")
	got := renderSystemdUnit("promo", "/usr/local/bin/shipyard-crew", paths)
	want, err := os.ReadFile(filepath.Join("testdata", "systemd_promo.service"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("unit mismatch:\n--got--\n%s\n--want--\n%s", got, string(want))
	}
}

func TestWriteAtomicWritesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := writeAtomic(p, []byte("a"), 0o600); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := writeAtomic(p, []byte("bb"), 0o600); err != nil {
		t.Fatalf("second: %v", err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "bb" {
		t.Errorf("contents = %q", string(data))
	}
}

func TestNewManagerPopulatesDefaults(t *testing.T) {
	m, err := NewManager()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if m.HomeDir == "" || m.Platform == "" || m.Runner == nil {
		t.Errorf("defaults not set: %+v", m)
	}
}

func TestDefaultRunnerExecutes(t *testing.T) {
	r := DefaultRunner()
	out, err := r.Run(context.Background(), "/bin/echo", "hello")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(string(out), "hello") {
		t.Errorf("out = %q", out)
	}
}
