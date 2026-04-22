package addon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeDetector struct {
	infos map[Kind]Info
	err   error
}

func (f fakeDetector) Detect(k Kind) (Info, error) {
	if f.err != nil {
		return Info{}, f.err
	}
	info, ok := f.infos[k]
	if !ok {
		return Info{Kind: k, Installed: false}, nil
	}
	return info, nil
}

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestRegistryLoadReturnsEmptyWhenFileMissing(t *testing.T) {
	t.Parallel()
	r := NewRegistry(t.TempDir())
	f, err := r.Load()
	if err != nil {
		t.Fatalf("Load() error = %v; want nil", err)
	}
	if f.SchemaVersion != RegistrySchemaVersion {
		t.Fatalf("SchemaVersion = %d; want %d", f.SchemaVersion, RegistrySchemaVersion)
	}
	if len(f.Addons) != 0 {
		t.Fatalf("Addons = %v; want empty", f.Addons)
	}
}

func TestRegistryRecordRoundTrips(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	r := &Registry{Home: home, Now: fixedNow(time.Unix(1700000000, 0).UTC())}

	if err := r.Record(KindCrew, true, "/tmp/bin/shipyard-crew", "0.2.4"); err != nil {
		t.Fatalf("Record crew: %v", err)
	}
	if err := r.Record(KindFairway, true, "/tmp/bin/shipyard-fairway", "1.1.3"); err != nil {
		t.Fatalf("Record fairway: %v", err)
	}

	f, err := r.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := f.Addons[KindCrew]; got == nil || got.Version != "0.2.4" {
		t.Fatalf("crew info = %+v", got)
	}
	if got := f.Addons[KindFairway]; got == nil || got.Version != "1.1.3" {
		t.Fatalf("fairway info = %+v", got)
	}
}

func TestRegistryForgetRemovesEntry(t *testing.T) {
	t.Parallel()
	r := &Registry{Home: t.TempDir(), Now: fixedNow(time.Unix(1700000000, 0).UTC())}
	_ = r.Record(KindCrew, true, "/x/shipyard-crew", "0.2.4")
	_ = r.Record(KindFairway, true, "/x/shipyard-fairway", "1.1.3")

	if err := r.Forget(KindCrew); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	f, _ := r.Load()
	if _, ok := f.Addons[KindCrew]; ok {
		t.Fatalf("crew still present after Forget")
	}
	if _, ok := f.Addons[KindFairway]; !ok {
		t.Fatalf("fairway was erroneously removed")
	}
}

func TestRegistryLoadRejectsUnknownSchema(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	bad := File{SchemaVersion: 999, Addons: map[Kind]*Info{}}
	body, _ := json.Marshal(bad)
	if err := os.WriteFile(filepath.Join(home, "addons.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(home)
	_, err := r.Load()
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("err = %v; want ErrUnsupportedSchema", err)
	}
}

func TestRegistrySaveIsAtomicPermission0600(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	r := &Registry{Home: home, Now: fixedNow(time.Unix(1700000000, 0).UTC())}
	if err := r.Record(KindCrew, true, "/x/bin", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, "addons.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o; want 0600", info.Mode().Perm())
	}
}

func TestDetectorFakeIntegration(t *testing.T) {
	t.Parallel()
	r := &Registry{
		Home: t.TempDir(),
		Detector: fakeDetector{infos: map[Kind]Info{
			KindCrew: {Kind: KindCrew, Installed: true, BinaryPath: "/faked", Version: "9.9.9"},
		}},
	}
	info, err := r.Detect(KindCrew)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !info.Installed || info.Version != "9.9.9" {
		t.Fatalf("info = %+v", info)
	}
	info2, _ := r.Detect(KindFairway)
	if info2.Installed {
		t.Fatalf("fairway should not be installed in this fake")
	}
}
