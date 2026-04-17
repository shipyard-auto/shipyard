package fairwayctl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/internal/service"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// makeTarGz creates an in-memory .tar.gz containing a single file named
// "shipyard-fairway" with the given content. Returns the bytes and their
// SHA-256 hex digest.
func makeTarGz(t *testing.T, content []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name:     "shipyard-fairway",
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0755,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()

	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

// fakeHTTPClient is an HTTPClient that serves pre-canned responses per URL.
type fakeHTTPClient struct {
	responses map[string]*http.Response
	err       error
}

func (f *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	if resp, ok := f.responses[req.URL.String()]; ok {
		return resp, nil
	}
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader("not found")),
	}, nil
}

func newFakeHTTPResponse(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

// fakeServiceAdder records calls for assertions.
type fakeServiceAdder struct {
	addErr    error
	removeErr error
	installed bool
	added     bool
	removed   bool
	addedPath string
}

func (f *fakeServiceAdder) AddFairway(execPath string) error {
	f.added = true
	f.addedPath = execPath
	return f.addErr
}

func (f *fakeServiceAdder) RemoveFairway() error {
	f.removed = true
	return f.removeErr
}

func (f *fakeServiceAdder) IsFairwayInstalled() (bool, error) {
	return f.installed, nil
}

// newTestInstaller builds an Installer wired to the given fake HTTP and service adder.
func newTestInstaller(t *testing.T, client HTTPClient, sa ServiceAdder) *Installer {
	t.Helper()
	dir := t.TempDir()
	return &Installer{
		Version:      "0.22",
		Platform:     Platform{OS: "linux", Arch: "amd64"},
		BinDir:       dir,
		HTTPClient:   client,
		ReleaseBase:  "https://fake.example/releases/download",
		ServiceAdder: sa,
	}
}

// makeFakeShellScript writes a minimal shell script to path that echoes version
// when invoked. Returns the path.
func makeFakeShellScript(t *testing.T, dir, name, output string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := fmt.Sprintf("#!/bin/sh\necho '%s'\n", output)
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

// ── ArtifactName tests ────────────────────────────────────────────────────────

func TestArtifactName_linuxAmd64(t *testing.T) {
	got := ArtifactName("0.22", Platform{OS: "linux", Arch: "amd64"})
	want := "shipyard-fairway_0.22_linux_amd64.tar.gz"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestArtifactName_darwinArm64(t *testing.T) {
	got := ArtifactName("0.22", Platform{OS: "darwin", Arch: "arm64"})
	want := "shipyard-fairway_0.22_darwin_arm64.tar.gz"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReleaseTag(t *testing.T) {
	got := ReleaseTag("0.22")
	want := "fairway-v0.22"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestChecksumManifestName(t *testing.T) {
	got := ChecksumManifestName("0.22")
	want := "shipyard-fairway_0.22_checksums.txt"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// ── Download tests ────────────────────────────────────────────────────────────

func TestDownload_streamsToTempFile(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 1024*1024) // 1 MB
	client := &fakeHTTPClient{
		responses: map[string]*http.Response{
			"https://fake.example/releases/download/v0.22/artifact.tar.gz": newFakeHTTPResponse(body),
		},
	}
	inst := newTestInstaller(t, client, &fakeServiceAdder{})

	path, err := inst.Download(context.Background(), "https://fake.example/releases/download/v0.22/artifact.tar.gz")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("temp file content mismatch (len got=%d want=%d)", len(got), len(body))
	}
}

func TestDownload_networkError_returnsError(t *testing.T) {
	client := &fakeHTTPClient{err: errors.New("network failure")}
	inst := newTestInstaller(t, client, &fakeServiceAdder{})

	_, err := inst.Download(context.Background(), "https://fake.example/anything")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ── VerifyChecksum tests ──────────────────────────────────────────────────────

func TestVerifyChecksum_valid(t *testing.T) {
	data := []byte("hello fairway")
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])

	f, _ := os.CreateTemp(t.TempDir(), "chk-*")
	f.Write(data)
	f.Close()

	if err := VerifyChecksum(f.Name(), expected); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestVerifyChecksum_mismatch_returnsError(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "chk-*")
	f.WriteString("data")
	f.Close()

	err := VerifyChecksum(f.Name(), strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
}

// ── ExtractBinary tests ───────────────────────────────────────────────────────

func TestExtractBinary_findsAndExtracts(t *testing.T) {
	content := []byte("#!/bin/sh\necho binary\n")
	archiveBytes, _ := makeTarGz(t, content)

	src, _ := os.CreateTemp(t.TempDir(), "arch-*")
	src.Write(archiveBytes)
	src.Close()

	dest := filepath.Join(t.TempDir(), "shipyard-fairway")
	if err := ExtractBinary(src.Name(), dest); err != nil {
		t.Fatalf("ExtractBinary: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content mismatch")
	}
}

func TestExtractBinary_malformedArchive_returnsError(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "bad-*")
	f.WriteString("this is not a tar.gz")
	f.Close()

	err := ExtractBinary(f.Name(), filepath.Join(t.TempDir(), "out"))
	if err == nil {
		t.Fatal("expected error for malformed archive, got nil")
	}
}

// ── InstalledVersion tests ────────────────────────────────────────────────────

func TestInstalledVersion_returnsVersion(t *testing.T) {
	dir := t.TempDir()
	makeFakeShellScript(t, dir, "shipyard-fairway", "0.22")

	inst := &Installer{Version: "0.22", BinDir: dir}
	ver, err := inst.InstalledVersion()
	if err != nil {
		t.Fatalf("InstalledVersion: %v", err)
	}
	if ver != "0.22" {
		t.Errorf("want 0.22, got %q", ver)
	}
}

func TestInstalledVersion_binAbsent_returnsError(t *testing.T) {
	inst := &Installer{Version: "0.22", BinDir: t.TempDir()}
	_, err := inst.InstalledVersion()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ── Install end-to-end tests ──────────────────────────────────────────────────

func buildInstallHTTPClient(t *testing.T, version string, p Platform) (*fakeHTTPClient, string) {
	t.Helper()
	content := []byte("#!/bin/sh\necho binary\n")
	archiveBytes, sha := makeTarGz(t, content)
	artifact := ArtifactName(version, p)
	base := "https://fake.example/releases/download"
	checksumLine := sha + "  " + artifact + "\n"

	return &fakeHTTPClient{
		responses: map[string]*http.Response{
			base + "/" + ReleaseTag(version) + "/" + artifact:              newFakeHTTPResponse(archiveBytes),
			base + "/" + ReleaseTag(version) + "/" + ChecksumManifestName(version): newFakeHTTPResponse([]byte(checksumLine)),
		},
	}, sha
}

func TestInstall_happyPath(t *testing.T) {
	client, _ := buildInstallHTTPClient(t, "0.22", Platform{OS: "linux", Arch: "amd64"})
	sa := &fakeServiceAdder{}
	inst := newTestInstaller(t, client, sa)

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Binary must be present in BinDir.
	if _, err := os.Stat(inst.BinPath()); err != nil {
		t.Errorf("binary not found after install: %v", err)
	}
	// Service must have been registered.
	if !sa.added {
		t.Error("ServiceAdder.AddFairway not called")
	}
	if sa.addedPath != inst.BinPath() {
		t.Errorf("AddFairway called with %q, want %q", sa.addedPath, inst.BinPath())
	}
}

func TestInstall_checksumFails_rollsBack(t *testing.T) {
	content := []byte("#!/bin/sh\necho binary\n")
	archiveBytes, _ := makeTarGz(t, content)
	artifact := ArtifactName("0.22", Platform{OS: "linux", Arch: "amd64"})
	base := "https://fake.example/releases/download"
	// Deliberately wrong checksum.
	wrongSHA := strings.Repeat("0", 64)
	checksumLine := wrongSHA + "  " + artifact + "\n"

	client := &fakeHTTPClient{
		responses: map[string]*http.Response{
			base + "/" + ReleaseTag("0.22") + "/" + artifact:                 newFakeHTTPResponse(archiveBytes),
			base + "/" + ReleaseTag("0.22") + "/" + ChecksumManifestName("0.22"): newFakeHTTPResponse([]byte(checksumLine)),
		},
	}
	sa := &fakeServiceAdder{}
	inst := newTestInstaller(t, client, sa)

	err := inst.Install(context.Background())
	if err == nil {
		t.Fatal("expected checksum error, got nil")
	}

	// Binary must NOT exist at destination.
	if _, statErr := os.Stat(inst.BinPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("binary should not exist after checksum failure")
	}
	// Service must NOT have been registered.
	if sa.added {
		t.Error("ServiceAdder.AddFairway must not be called after checksum failure")
	}
}

func TestInstall_alreadyInstalledSameVersion_returnsNoOp(t *testing.T) {
	dir := t.TempDir()
	makeFakeShellScript(t, dir, "shipyard-fairway", "0.22")

	inst := &Installer{
		Version:      "0.22",
		Platform:     Platform{OS: "linux", Arch: "amd64"},
		BinDir:       dir,
		HTTPClient:   &fakeHTTPClient{},
		ReleaseBase:  "https://fake.example/releases/download",
		ServiceAdder: &fakeServiceAdder{},
	}

	err := inst.Install(context.Background())
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("want ErrAlreadyInstalled, got %v", err)
	}
}

func TestInstall_alreadyInstalledDifferentVersion_returnsSuggestUpgrade(t *testing.T) {
	dir := t.TempDir()
	makeFakeShellScript(t, dir, "shipyard-fairway", "0.21")

	inst := &Installer{
		Version:      "0.22",
		Platform:     Platform{OS: "linux", Arch: "amd64"},
		BinDir:       dir,
		HTTPClient:   &fakeHTTPClient{},
		ReleaseBase:  "https://fake.example/releases/download",
		ServiceAdder: &fakeServiceAdder{},
	}

	err := inst.Install(context.Background())
	if !errors.Is(err, ErrUpgradeRequired) {
		t.Errorf("want ErrUpgradeRequired, got %v", err)
	}
}

func TestInstall_serviceRegisterFails_removesBinary(t *testing.T) {
	client, _ := buildInstallHTTPClient(t, "0.22", Platform{OS: "linux", Arch: "amd64"})
	sa := &fakeServiceAdder{addErr: errors.New("service registration failed")}
	inst := newTestInstaller(t, client, sa)

	err := inst.Install(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Binary must be removed (rollback).
	if _, statErr := os.Stat(inst.BinPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("binary should be removed after service registration failure")
	}
}

func TestInstall_createsBinDirIfMissing(t *testing.T) {
	client, _ := buildInstallHTTPClient(t, "0.22", Platform{OS: "linux", Arch: "amd64"})
	sa := &fakeServiceAdder{}

	// BinDir does not exist yet.
	binDir := filepath.Join(t.TempDir(), "new", "bin")
	inst := &Installer{
		Version:      "0.22",
		Platform:     Platform{OS: "linux", Arch: "amd64"},
		BinDir:       binDir,
		HTTPClient:   client,
		ReleaseBase:  "https://fake.example/releases/download",
		ServiceAdder: sa,
	}

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(binDir); err != nil {
		t.Errorf("BinDir not created: %v", err)
	}
}

func TestCurrentPlatform_returnsNonEmpty(t *testing.T) {
	p := CurrentPlatform()
	if p.OS == "" {
		t.Error("OS is empty")
	}
	if p.Arch == "" {
		t.Error("Arch is empty")
	}
}

func TestUninstall_removesServiceAndBinary(t *testing.T) {
	dir := t.TempDir()
	// Create a real binary so Remove can delete it.
	binPath := filepath.Join(dir, "shipyard-fairway")
	if err := os.WriteFile(binPath, []byte("bin"), 0755); err != nil {
		t.Fatal(err)
	}
	sa := &fakeServiceAdder{}
	inst := &Installer{Version: "0.22", BinDir: dir, ServiceAdder: sa}

	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !sa.removed {
		t.Error("RemoveFairway not called")
	}
	if _, err := os.Stat(binPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("binary still exists after uninstall")
	}
}

func TestUninstall_binaryAbsent_noError(t *testing.T) {
	sa := &fakeServiceAdder{}
	inst := &Installer{Version: "0.22", BinDir: t.TempDir(), ServiceAdder: sa}

	if err := inst.Uninstall(context.Background()); err != nil {
		t.Errorf("expected nil for absent binary, got %v", err)
	}
}


func TestExtractBinary_noBinaryInArchive_returnsError(t *testing.T) {
	// Create a tar.gz with a file that is NOT named "shipyard-fairway".
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{Name: "other-file", Typeflag: tar.TypeReg, Size: 4, Mode: 0644}
	tw.WriteHeader(hdr)
	tw.Write([]byte("data"))
	tw.Close()
	gw.Close()

	src, _ := os.CreateTemp(t.TempDir(), "arch-*")
	src.Write(buf.Bytes())
	src.Close()

	err := ExtractBinary(src.Name(), filepath.Join(t.TempDir(), "out"))
	if err == nil {
		t.Fatal("expected error when binary not in archive")
	}
}

func TestDownload_httpNonOK_returnsError(t *testing.T) {
	client := &fakeHTTPClient{
		responses: map[string]*http.Response{
			"https://fake.example/404": {
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("not found")),
			},
		},
	}
	inst := newTestInstaller(t, client, &fakeServiceAdder{})
	_, err := inst.Download(context.Background(), "https://fake.example/404")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestExtractSHA_notFound_returnsError(t *testing.T) {
	// checksums.txt without the expected artifact
	f, _ := os.CreateTemp(t.TempDir(), "chk-*")
	f.WriteString("abc123  other-artifact.tar.gz\n")
	f.Close()

	_, err := extractSHA(f.Name(), "missing-artifact.tar.gz")
	if err == nil {
		t.Fatal("expected error when artifact not in checksums")
	}
}

func TestInstall_atomicRename(t *testing.T) {
	client, _ := buildInstallHTTPClient(t, "0.22", Platform{OS: "linux", Arch: "amd64"})
	sa := &fakeServiceAdder{}
	inst := newTestInstaller(t, client, sa)

	renameErr := errors.New("rename failed")
	inst.rename = func(old, new string) error { return renameErr }

	err := inst.Install(context.Background())
	if err == nil {
		t.Fatal("expected rename error, got nil")
	}

	// No binary at destination after failed rename.
	if _, statErr := os.Stat(inst.BinPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("binary should not exist at destination after rename failure")
	}
	// Temp binary must also have been cleaned up (deferred os.Remove).
	matches, _ := filepath.Glob(filepath.Join(inst.BinDir, ".shipyard-fairway-*"))
	if len(matches) != 0 {
		t.Errorf("temp files not cleaned up: %v", matches)
	}
}

// ── serviceAdderImpl tests ────────────────────────────────────────────────────

// testServiceRepo is an in-memory service.Repository for tests.
type testServiceRepo struct {
	store service.Store
}

func (r *testServiceRepo) Load() (service.Store, error) {
	if r.store.Services == nil {
		r.store = service.Store{Version: 1, Services: []service.ServiceRecord{}}
	}
	return r.store, nil
}

func (r *testServiceRepo) Save(s service.Store) error {
	r.store = s
	return nil
}

// testServiceManager is a no-op service.Manager for tests.
type testServiceManager struct{}

func (m *testServiceManager) Platform() service.Platform           { return "test" }
func (m *testServiceManager) Sync(_ []service.ServiceRecord) error { return nil }
func (m *testServiceManager) Reload() error                        { return nil }
func (m *testServiceManager) Start(_ string) error                 { return nil }
func (m *testServiceManager) Stop(_ string) error                  { return nil }
func (m *testServiceManager) Restart(_ string) error               { return nil }
func (m *testServiceManager) Status(_ string) (service.RuntimeStatus, error) {
	return service.RuntimeStatus{}, nil
}
func (m *testServiceManager) Enable(_ string) error  { return nil }
func (m *testServiceManager) Disable(_ string) error { return nil }
func (m *testServiceManager) Remove(_ string) error  { return nil }

func newTestServiceAdder() *serviceAdderImpl {
	repo := &testServiceRepo{}
	svc := service.Service{
		Repo:    repo,
		Manager: &testServiceManager{},
		IDGen:   service.RandomIDGenerator{},
		Now:     time.Now,
	}
	return &serviceAdderImpl{svc: svc}
}

func TestServiceAdderImpl_addAndDetect(t *testing.T) {
	sa := newTestServiceAdder()

	installed, err := sa.IsFairwayInstalled()
	if err != nil || installed {
		t.Fatalf("expected not installed initially, got installed=%v err=%v", installed, err)
	}

	if err := sa.AddFairway("/usr/local/bin/shipyard-fairway"); err != nil {
		t.Fatalf("AddFairway: %v", err)
	}

	installed, err = sa.IsFairwayInstalled()
	if err != nil {
		t.Fatalf("IsFairwayInstalled: %v", err)
	}
	if !installed {
		t.Error("expected installed=true after AddFairway")
	}
}

func TestServiceAdderImpl_remove(t *testing.T) {
	sa := newTestServiceAdder()

	if err := sa.AddFairway("/usr/local/bin/shipyard-fairway"); err != nil {
		t.Fatalf("AddFairway: %v", err)
	}

	if err := sa.RemoveFairway(); err != nil {
		t.Fatalf("RemoveFairway: %v", err)
	}

	installed, err := sa.IsFairwayInstalled()
	if err != nil || installed {
		t.Errorf("expected not installed after remove, got installed=%v err=%v", installed, err)
	}
}

func TestServiceAdderImpl_removeWhenNotInstalled_noError(t *testing.T) {
	sa := newTestServiceAdder()
	if err := sa.RemoveFairway(); err != nil {
		t.Errorf("RemoveFairway when not installed should be no-op, got %v", err)
	}
}

// ── Task 014 — Uninstall / Upgrade tests ─────────────────────────────────────

func newTestInstaller014(t *testing.T, client HTTPClient, sa ServiceAdder) *Installer {
	t.Helper()
	dir := t.TempDir()
	return &Installer{
		Version:      "0.22",
		Platform:     Platform{OS: "linux", Arch: "amd64"},
		BinDir:       dir,
		StateDir:     filepath.Join(dir, "state"),
		RunDir:       filepath.Join(dir, "run"),
		HTTPClient:   client,
		ReleaseBase:  "https://fake.example/releases/download",
		ServiceAdder: sa,
	}
}

func TestUninstall_happyPath_removesBinaryAndService(t *testing.T) {
	inst := newTestInstaller014(t, &fakeHTTPClient{}, &fakeServiceAdder{})
	// Write a real binary so os.Remove can delete it.
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0755); err != nil {
		t.Fatal(err)
	}
	sa := inst.ServiceAdder.(*fakeServiceAdder)

	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !sa.removed {
		t.Error("RemoveFairway not called")
	}
	if _, err := os.Stat(inst.BinPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("binary still exists after uninstall")
	}
}

func TestUninstall_keepState_preservesStateDir(t *testing.T) {
	inst := newTestInstaller014(t, &fakeHTTPClient{}, &fakeServiceAdder{})
	inst.Purge = false

	if err := os.MkdirAll(inst.StateDir, 0755); err != nil {
		t.Fatal(err)
	}
	routesFile := filepath.Join(inst.StateDir, "routes.json")
	if err := os.WriteFile(routesFile, []byte(`{"schemaVersion":"1"}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if _, err := os.Stat(routesFile); err != nil {
		t.Errorf("routes.json should be preserved, got %v", err)
	}
}

func TestUninstall_purge_removesStateDir(t *testing.T) {
	inst := newTestInstaller014(t, &fakeHTTPClient{}, &fakeServiceAdder{})
	inst.Purge = true

	if err := os.MkdirAll(inst.StateDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inst.StateDir, "routes.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if _, err := os.Stat(inst.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Error("StateDir should be removed with --purge")
	}
}

func TestUninstall_notInstalled_returnsNoOp(t *testing.T) {
	inst := newTestInstaller014(t, &fakeHTTPClient{}, &fakeServiceAdder{})
	// No binary, no state, nothing.
	if err := inst.Uninstall(context.Background()); err != nil {
		t.Errorf("expected no-op, got %v", err)
	}
}

func TestUninstall_serviceRemoveFails_continuesAndReportsWarning(t *testing.T) {
	sa := &fakeServiceAdder{removeErr: errors.New("daemon unreachable")}
	inst := newTestInstaller014(t, &fakeHTTPClient{}, sa)
	// Write a binary to confirm removal continues despite service error.
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0755); err != nil {
		t.Fatal(err)
	}

	err := inst.Uninstall(context.Background())
	// Must return an error (the warning).
	if err == nil {
		t.Fatal("expected warning error from service removal failure")
	}
	// Binary must still be removed (cleanup continued).
	if _, statErr := os.Stat(inst.BinPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("binary should be removed even when service removal fails")
	}
}

func TestUninstall_removesSocketAndPidfile(t *testing.T) {
	inst := newTestInstaller014(t, &fakeHTTPClient{}, &fakeServiceAdder{})

	if err := os.MkdirAll(inst.RunDir, 0755); err != nil {
		t.Fatal(err)
	}
	sockPath := filepath.Join(inst.RunDir, "fairway.sock")
	pidPath := filepath.Join(inst.RunDir, "fairway.pid")
	os.WriteFile(sockPath, []byte{}, 0600)
	os.WriteFile(pidPath, []byte("1234"), 0600)

	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	for _, p := range []string{sockPath, pidPath} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("orphan file should be removed: %s", p)
		}
	}
}

func TestUpgrade_sameVersion_returnsNoOp(t *testing.T) {
	inst := newTestInstaller014(t, &fakeHTTPClient{}, &fakeServiceAdder{})
	// Write binary that reports the same version.
	makeFakeShellScript(t, inst.BinDir, "shipyard-fairway", "0.22")

	err := inst.Upgrade(context.Background())
	if !errors.Is(err, ErrAlreadyAtVersion) {
		t.Errorf("want ErrAlreadyAtVersion, got %v", err)
	}
}

func TestUpgrade_differentVersion_uninstallsThenInstalls(t *testing.T) {
	client, _ := buildInstallHTTPClient(t, "0.22", Platform{OS: "linux", Arch: "amd64"})
	sa := &fakeServiceAdder{}
	inst := newTestInstaller014(t, client, sa)
	// Binary reports older version.
	makeFakeShellScript(t, inst.BinDir, "shipyard-fairway", "0.21")

	if err := inst.Upgrade(context.Background()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if !sa.removed {
		t.Error("RemoveFairway not called during upgrade")
	}
	if !sa.added {
		t.Error("AddFairway not called during upgrade")
	}
	if _, err := os.Stat(inst.BinPath()); err != nil {
		t.Errorf("binary should exist after upgrade: %v", err)
	}
}

func TestUpgrade_stateDirPreserved(t *testing.T) {
	client, _ := buildInstallHTTPClient(t, "0.22", Platform{OS: "linux", Arch: "amd64"})
	inst := newTestInstaller014(t, client, &fakeServiceAdder{})
	makeFakeShellScript(t, inst.BinDir, "shipyard-fairway", "0.21")

	if err := os.MkdirAll(inst.StateDir, 0755); err != nil {
		t.Fatal(err)
	}
	routesFile := filepath.Join(inst.StateDir, "routes.json")
	os.WriteFile(routesFile, []byte(`{"schemaVersion":"1"}`), 0644)

	if err := inst.Upgrade(context.Background()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if _, err := os.Stat(routesFile); err != nil {
		t.Errorf("routes.json must survive upgrade: %v", err)
	}
}

func TestUpgrade_installFailsAfterUninstall_reportClearError(t *testing.T) {
	// Stub HTTPClient returns an error — install will fail.
	client := &fakeHTTPClient{err: errors.New("network failure")}
	inst := newTestInstaller014(t, client, &fakeServiceAdder{})
	// Binary absent so version check is skipped, uninstall is a no-op.

	err := inst.Upgrade(context.Background())
	if err == nil {
		t.Fatal("expected error when install fails after uninstall")
	}
	if !strings.Contains(err.Error(), "recover") {
		t.Errorf("error should suggest recovery, got: %v", err)
	}
}
