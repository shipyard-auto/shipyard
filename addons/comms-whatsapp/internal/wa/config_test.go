package wa

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseDurationAndTTL(t *testing.T) {
	ok := map[string]time.Duration{"14d": 14 * 24 * time.Hour, "36h": 36 * time.Hour, "90m": 90 * time.Minute, " 1d ": 24 * time.Hour}
	for in, want := range ok {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q)=%v,%v want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "-1d", "1.5d", "abc", "-2h"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) want error", in)
		}
	}
	for _, in := range []string{"30m", "91d"} {
		if _, err := ParseTTL(in); err == nil {
			t.Errorf("ParseTTL(%q) want range error", in)
		}
	}
	if got := FormatDuration(48 * time.Hour); got != "2d" {
		t.Errorf("FormatDuration=%q", got)
	}
	if got := FormatDuration(3 * time.Hour); got != "3h" {
		t.Errorf("FormatDuration=%q", got)
	}
}

func TestConfigRoundTripAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c, err := LoadConfig(path)
	if err != nil || c != DefaultConfig() {
		t.Fatalf("missing file: %+v %v", c, err)
	}
	if err := c.Set("default-ttl", "7d"); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("device-name", "Servidor"); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%o", info.Mode().Perm())
	}
	got, err := LoadConfig(path)
	if err != nil || got.DefaultTTL != "7d" || got.DeviceName != "Servidor" || got.DefaultTTLDuration() != 7*24*time.Hour {
		t.Fatalf("loaded=%+v %v", got, err)
	}
	for _, kv := range [][2]string{{"default-ttl", "200d"}, {"device-name", ""}, {"color", "blue"}} {
		c := DefaultConfig()
		if err := c.Set(kv[0], kv[1]); err == nil {
			t.Errorf("Set(%q,%q) want error", kv[0], kv[1])
		}
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"device_name":"x","default_ttl":"1d"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("want version error")
	}
}

func TestTrackerPersistsAndRunningDetection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(path, os.Getpid(), func() time.Time { return now })
	tr.SetState(StateConnected, "")
	tr.SetAccount("5511900000000", "Leo")
	tr.MessageSeen(now.Add(-time.Minute))
	if err := tr.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	st, ok, err := ReadStatus(path)
	if err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	if st.State != StateConnected || st.Account != "5511900000000" || st.LastMessageAt.IsZero() {
		t.Fatalf("status=%+v", st)
	}
	if !st.Running(now) {
		t.Fatal("own pid with fresh heartbeat should be running")
	}
	if st.Running(now.Add(3 * HeartbeatInterval)) {
		t.Fatal("stale heartbeat must not count as running")
	}
	st.State = StateStopped
	if st.Running(now) {
		t.Fatal("stopped must not count as running")
	}
	if _, ok, err := ReadStatus(filepath.Join(t.TempDir(), "none.json")); ok || err != nil {
		t.Fatalf("missing status: ok=%v err=%v", ok, err)
	}
}

func TestLockIsExclusive(t *testing.T) {
	p := PathsAt(filepath.Join(t.TempDir(), "wa"))
	l1, err := AcquireLock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(p); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("second lock: %v", err)
	}
	l1.Release()
	l2, err := AcquireLock(p)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l2.Release()
	info, _ := os.Stat(p.Dir)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm=%o", info.Mode().Perm())
	}
}

func TestBuildReportCreatesNothing(t *testing.T) {
	p := PathsAt(filepath.Join(t.TempDir(), "wa"))
	r, err := BuildReport(context.Background(), p, DefaultConfig(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Paired || r.Running || r.Daemon != nil {
		t.Fatalf("report=%+v", r)
	}
	if _, err := os.Stat(p.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created %s", p.Dir)
	}
}

func TestServeWithoutPairingExits20WithoutCreatingSession(t *testing.T) {
	p := PathsAt(filepath.Join(t.TempDir(), "wa"))
	err := Serve(context.Background(), ServeOptions{Paths: p, Config: DefaultConfig(), Log: newHarness(t).h.Log})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != ExitNotPaired {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(p.Session); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("serve created session.db")
	}
	// A second process holding the lock means "already running".
	l, _ := AcquireLock(p)
	defer l.Release()
	err = Serve(context.Background(), ServeOptions{Paths: p, Config: DefaultConfig(), Log: newHarness(t).h.Log})
	if !errors.As(err, &exit) || exit.Code != ExitAlreadyRunning {
		t.Fatalf("locked err=%v", err)
	}
}
