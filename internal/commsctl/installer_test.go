package commsctl

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
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// makeTarGz creates an in-memory .tar.gz containing a single file named
// "shipyard-comms" with the given content. Returns the bytes and their
// SHA-256 hex digest.
func makeTarGz(t *testing.T, content []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name:     BinaryName,
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

// fakeHTTPClient serves pre-canned responses per URL.
type fakeHTTPClient struct {
	responses map[string]func() *http.Response
	err       error
}

func (f *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	if fn, ok := f.responses[req.URL.String()]; ok {
		return fn(), nil
	}
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader("not found")),
	}, nil
}

func okResponse(body []byte) func() *http.Response {
	return func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
		}
	}
}

func newTestInstaller(t *testing.T, version string) (*Installer, string) {
	t.Helper()
	binDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	return &Installer{
		Version:     version,
		Platform:    Platform{OS: "linux", Arch: "amd64"},
		BinDir:      binDir,
		StateDir:    stateDir,
		ReleaseBase: "https://example.test/releases",
		Now:         time.Now,
	}, binDir
}

func setupHappyPath(t *testing.T, inst *Installer) []byte {
	t.Helper()
	tarBytes, sha := makeTarGz(t, []byte("#!/bin/sh\necho stub\n"))
	artifact := ArtifactName(inst.Version, inst.Platform)
	checksums := fmt.Sprintf("%s  %s\n", sha, artifact)

	tag := ReleaseTag(inst.Version)
	artifactURL := fmt.Sprintf("%s/%s/%s", inst.ReleaseBase, tag, artifact)
	checksumURL := fmt.Sprintf("%s/%s/%s", inst.ReleaseBase, tag, ChecksumManifestName(inst.Version))

	inst.HTTPClient = &fakeHTTPClient{
		responses: map[string]func() *http.Response{
			artifactURL: okResponse(tarBytes),
			checksumURL: okResponse([]byte(checksums)),
		},
	}
	return tarBytes
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestInstall_HappyPath(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.1.0")
	setupHappyPath(t, inst)

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	binPath := filepath.Join(binDir, BinaryName)
	info, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("stat binary: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: mode=%o", info.Mode().Perm())
	}
}

func TestInstall_AlreadyInstalled(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.1.0")
	// Plant a "binary" that responds with the same version when called.
	binPath := filepath.Join(binDir, BinaryName)
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 0.1.0; exit 0; fi\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := inst.Install(context.Background())
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("expected ErrAlreadyInstalled, got %v", err)
	}
}

func TestInstall_UpgradeRequired(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.2.0")
	binPath := filepath.Join(binDir, BinaryName)
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 0.1.0; exit 0; fi\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := inst.Install(context.Background())
	if !errors.Is(err, ErrUpgradeRequired) {
		t.Errorf("expected ErrUpgradeRequired, got %v", err)
	}
	if !strings.Contains(err.Error(), "0.1.0") || !strings.Contains(err.Error(), "0.2.0") {
		t.Errorf("error should cite both versions, got %v", err)
	}
}

func TestInstall_ForceOverridesAlreadyInstalled(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.1.0")
	inst.Force = true
	setupHappyPath(t, inst)

	binPath := filepath.Join(binDir, BinaryName)
	if err := os.WriteFile(binPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install with Force: %v", err)
	}
	// Confirm overwrite happened (new content is the extracted stub, not "old").
	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) == "old" {
		t.Errorf("Force=true did not overwrite binary")
	}
}

func TestInstall_ChecksumMismatch(t *testing.T) {
	inst, _ := newTestInstaller(t, "0.1.0")
	tarBytes, _ := makeTarGz(t, []byte("stub"))
	artifact := ArtifactName(inst.Version, inst.Platform)
	// Wrong checksum in the manifest.
	checksums := fmt.Sprintf("0000000000000000000000000000000000000000000000000000000000000000  %s\n", artifact)

	tag := ReleaseTag(inst.Version)
	inst.HTTPClient = &fakeHTTPClient{
		responses: map[string]func() *http.Response{
			fmt.Sprintf("%s/%s/%s", inst.ReleaseBase, tag, artifact):                           okResponse(tarBytes),
			fmt.Sprintf("%s/%s/%s", inst.ReleaseBase, tag, ChecksumManifestName(inst.Version)): okResponse([]byte(checksums)),
		},
	}

	err := inst.Install(context.Background())
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("expected checksum error, got %v", err)
	}
}

func TestInstall_DownloadFailure(t *testing.T) {
	inst, _ := newTestInstaller(t, "0.1.0")
	inst.HTTPClient = &fakeHTTPClient{err: errors.New("network down")}

	err := inst.Install(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "network down") {
		t.Errorf("expected network error to surface, got %v", err)
	}
}

func TestUninstall_RemovesBinary(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.1.0")
	binPath := filepath.Join(binDir, BinaryName)
	if err := os.WriteFile(binPath, []byte("x"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(binPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("binary still present after Uninstall, stat err=%v", err)
	}
}

func TestUninstall_MissingBinaryIsIdempotent(t *testing.T) {
	inst, _ := newTestInstaller(t, "0.1.0")
	if err := inst.Uninstall(context.Background()); err != nil {
		t.Errorf("Uninstall on absent binary: %v", err)
	}
}

func TestUninstall_PurgePreservesStateByDefault(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.1.0")
	binPath := filepath.Join(binDir, BinaryName)
	if err := os.WriteFile(binPath, []byte("x"), 0o755); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	// Seed state dir.
	if err := os.MkdirAll(inst.StateDir, 0o700); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inst.StateDir, "channels.json"), []byte("[]"), 0o600); err != nil {
		t.Fatalf("seed state file: %v", err)
	}

	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(inst.StateDir); err != nil {
		t.Errorf("state dir must be preserved when Purge=false, got stat err=%v", err)
	}
}

func TestUninstall_PurgeRemovesStateDir(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.1.0")
	inst.Purge = true
	binPath := filepath.Join(binDir, BinaryName)
	if err := os.WriteFile(binPath, []byte("x"), 0o755); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	if err := os.MkdirAll(inst.StateDir, 0o700); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(inst.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state dir should be removed when Purge=true, stat err=%v", err)
	}
}

func TestIsInstalled_FileExists(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.1.0")
	if inst.IsInstalled() {
		t.Errorf("IsInstalled should be false when binary missing")
	}
	if err := os.WriteFile(filepath.Join(binDir, BinaryName), []byte("x"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !inst.IsInstalled() {
		t.Errorf("IsInstalled should be true when binary present")
	}
}

func TestIsInstalled_RejectsDirectory(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.1.0")
	if err := os.MkdirAll(filepath.Join(binDir, BinaryName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if inst.IsInstalled() {
		t.Errorf("IsInstalled should be false for a directory at BinPath")
	}
}

func TestInstalledVersion_ParsesPlainOutput(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.1.0")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 0.1.0; exit 0; fi\n"
	if err := os.WriteFile(filepath.Join(binDir, BinaryName), []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := inst.InstalledVersion()
	if err != nil {
		t.Fatalf("InstalledVersion: %v", err)
	}
	if got != "0.1.0" {
		t.Errorf("got %q, want 0.1.0", got)
	}
}

func TestInstalledVersion_ParsesPrefixedOutput(t *testing.T) {
	// Confirm parseVersionOutput tolerates the default header format too.
	got := parseVersionOutput("shipyard-comms 0.5.2 (abc, built 2026-05-24)\n")
	if got != "0.5.2" {
		t.Errorf("got %q, want 0.5.2", got)
	}
}

func TestInstalledVersion_MissingBinary(t *testing.T) {
	inst, _ := newTestInstaller(t, "0.1.0")
	_, err := inst.InstalledVersion()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestExtractBinary_MissingMember(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	payload := []byte("not-the-binary\n")
	hdr := &tar.Header{Name: "other-file", Typeflag: tar.TypeReg, Size: int64(len(payload)), Mode: 0o755}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	tw.Close()
	gw.Close()

	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.tar.gz")
	if err := os.WriteFile(src, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := ExtractBinary(src, filepath.Join(tmp, "dest"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), BinaryName) {
		t.Errorf("error should mention %s, got %v", BinaryName, err)
	}
}

func TestVerifyChecksum(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x")
	content := []byte("hello world")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sum := sha256.Sum256(content)
	expected := hex.EncodeToString(sum[:])

	if err := VerifyChecksum(path, expected); err != nil {
		t.Errorf("expected match: %v", err)
	}
	if err := VerifyChecksum(path, strings.Repeat("0", 64)); err == nil {
		t.Error("expected mismatch, got nil")
	}
}

func TestUpgrade_AlreadyAtVersion(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.1.0")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 0.1.0; exit 0; fi\n"
	if err := os.WriteFile(filepath.Join(binDir, BinaryName), []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := inst.Upgrade(context.Background())
	if !errors.Is(err, ErrAlreadyAtVersion) {
		t.Errorf("expected ErrAlreadyAtVersion, got %v", err)
	}
}
