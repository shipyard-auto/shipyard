package crewctl

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
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// makeTarGz builds an in-memory .tar.gz containing a single file named
// "shipyard-crew" with the given content. Returns bytes and SHA-256 hex.
func makeTarGz(t *testing.T, content []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name:     BinaryName,
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0o755,
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

// loadScriptFixture returns the shell-script bytes used as the fake binary.
// The fixture lives in testdata/ so the shape of the release archive is
// anchored to a checked-in artefact.
func loadScriptFixture(t *testing.T, version string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fake-crew.sh"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return []byte(fmt.Sprintf("#!/bin/sh\nexport SHIPYARD_CREW_VERSION=%s\n%s",
		version, strings.TrimPrefix(string(raw), "#!/bin/sh\n")))
}

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

func stockResponse(status int, body []byte) func() *http.Response {
	return func() *http.Response {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(bytes.NewReader(body)),
		}
	}
}

func newTestInstaller(t *testing.T, client HTTPClient) *Installer {
	t.Helper()
	base := t.TempDir()
	return &Installer{
		Version:     "0.1.0",
		Platform:    Platform{OS: "linux", Arch: "amd64"},
		BinDir:      filepath.Join(base, "bin"),
		StateDir:    filepath.Join(base, "state"),
		RunDir:      filepath.Join(base, "run"),
		HTTPClient:  client,
		ReleaseBase: "https://fake.example/releases/download",
		Warn:        io.Discard,
	}
}

func buildHappyClient(t *testing.T, version string, p Platform) *fakeHTTPClient {
	t.Helper()
	content := loadScriptFixture(t, version)
	archive, sha := makeTarGz(t, content)
	artifact := ArtifactName(version, p)
	base := "https://fake.example/releases/download"
	checksumLine := sha + "  " + artifact + "\n"

	return &fakeHTTPClient{
		responses: map[string]func() *http.Response{
			base + "/" + ReleaseTag(version) + "/" + artifact:                      stockResponse(200, archive),
			base + "/" + ReleaseTag(version) + "/" + ChecksumManifestName(version): stockResponse(200, []byte(checksumLine)),
		},
	}
}

// ── Artifact name / release tag tests ─────────────────────────────────────────

func TestArtifactName(t *testing.T) {
	cases := []struct {
		version string
		p       Platform
		want    string
	}{
		{"0.1.0", Platform{"linux", "amd64"}, "shipyard-crew_0.1.0_linux_amd64.tar.gz"},
		{"0.1.0", Platform{"linux", "arm64"}, "shipyard-crew_0.1.0_linux_arm64.tar.gz"},
		{"0.1.0", Platform{"darwin", "amd64"}, "shipyard-crew_0.1.0_darwin_amd64.tar.gz"},
		{"0.1.0", Platform{"darwin", "arm64"}, "shipyard-crew_0.1.0_darwin_arm64.tar.gz"},
	}
	for _, c := range cases {
		got := ArtifactName(c.version, c.p)
		if got != c.want {
			t.Errorf("ArtifactName(%s,%v) = %q, want %q", c.version, c.p, got, c.want)
		}
	}
}

func TestReleaseTag(t *testing.T) {
	if got := ReleaseTag("0.1.0"); got != "crew-v0.1.0" {
		t.Fatalf("got %q, want crew-v0.1.0", got)
	}
}

func TestChecksumManifestName(t *testing.T) {
	if got := ChecksumManifestName("0.1.0"); got != "shipyard-crew_0.1.0_checksums.txt" {
		t.Fatalf("got %q", got)
	}
}

func TestCurrentPlatform_nonEmpty(t *testing.T) {
	p := CurrentPlatform()
	if p.OS == "" || p.Arch == "" {
		t.Errorf("empty platform: %+v", p)
	}
}

func TestPlatform_Validate(t *testing.T) {
	if err := (Platform{"linux", "amd64"}).Validate(); err != nil {
		t.Errorf("linux/amd64 should be supported: %v", err)
	}
	err := (Platform{"windows", "amd64"}).Validate()
	if !errors.Is(err, ErrPlatformUnsupported) {
		t.Errorf("windows/amd64 should fail with ErrPlatformUnsupported, got %v", err)
	}
}

// ── parseVersionOutput tests ──────────────────────────────────────────────────

func TestParseVersionOutput(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"shipyard-crew 0.1.0 (abc123, built 2026-04-20)", "0.1.0"},
		{"shipyard-crew 0.1.0", "0.1.0"},
		{"0.1.0", "0.1.0"},
		{"  0.2.0\n", "0.2.0"},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseVersionOutput(c.in); got != c.want {
			t.Errorf("parseVersionOutput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── Download tests ────────────────────────────────────────────────────────────

func TestDownload_streamsToTempFile(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 512*1024)
	client := &fakeHTTPClient{
		responses: map[string]func() *http.Response{
			"https://fake.example/a": stockResponse(200, body),
		},
	}
	inst := newTestInstaller(t, client)

	path, err := inst.download(context.Background(), "https://fake.example/a")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer os.Remove(path)

	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, body) {
		t.Errorf("body mismatch len=%d want=%d", len(got), len(body))
	}
}

func TestDownload_networkError(t *testing.T) {
	client := &fakeHTTPClient{err: errors.New("boom")}
	inst := newTestInstaller(t, client)
	if _, err := inst.download(context.Background(), "https://fake.example/a"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDownload_nonOK(t *testing.T) {
	client := &fakeHTTPClient{
		responses: map[string]func() *http.Response{
			"https://fake.example/404": stockResponse(404, []byte("nope")),
		},
	}
	inst := newTestInstaller(t, client)
	if _, err := inst.download(context.Background(), "https://fake.example/404"); err == nil {
		t.Fatal("expected error for 404")
	}
}

// ── Checksum / extract tests ──────────────────────────────────────────────────

func TestVerifyChecksum_valid(t *testing.T) {
	data := []byte("hello crew")
	sum := sha256.Sum256(data)
	f, _ := os.CreateTemp(t.TempDir(), "chk-*")
	f.Write(data)
	f.Close()
	if err := verifyChecksum(f.Name(), hex.EncodeToString(sum[:])); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestVerifyChecksum_mismatch(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "chk-*")
	f.WriteString("data")
	f.Close()
	if err := verifyChecksum(f.Name(), strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected mismatch")
	}
}

func TestExtractBinary_happyPath(t *testing.T) {
	content := []byte("#!/bin/sh\necho hi\n")
	archive, _ := makeTarGz(t, content)

	src, _ := os.CreateTemp(t.TempDir(), "arch-*")
	src.Write(archive)
	src.Close()

	dest := filepath.Join(t.TempDir(), BinaryName)
	if err := extractBinary(src.Name(), dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, content) {
		t.Error("content mismatch")
	}
}

func TestExtractBinary_malformed(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "bad-*")
	f.WriteString("not a tar.gz")
	f.Close()
	if err := extractBinary(f.Name(), filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractBinary_noBinaryInArchive(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{Name: "other", Typeflag: tar.TypeReg, Size: 3})
	tw.Write([]byte("abc"))
	tw.Close()
	gw.Close()

	src, _ := os.CreateTemp(t.TempDir(), "arch-*")
	src.Write(buf.Bytes())
	src.Close()

	if err := extractBinary(src.Name(), filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractSHA_multiplePlatforms_selectsCorrect(t *testing.T) {
	content := []byte(strings.Join([]string{
		"aaa  shipyard-crew_0.1.0_linux_arm64.tar.gz",
		"bbb  shipyard-crew_0.1.0_linux_amd64.tar.gz",
		"ccc  shipyard-crew_0.1.0_darwin_amd64.tar.gz",
		"",
	}, "\n"))
	f, _ := os.CreateTemp(t.TempDir(), "chk-*")
	f.Write(content)
	f.Close()

	got, err := extractSHA(f.Name(), "shipyard-crew_0.1.0_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "bbb" {
		t.Errorf("want bbb, got %q", got)
	}
}

func TestExtractSHA_notFound(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "chk-*")
	f.WriteString("abc  other.tar.gz\n")
	f.Close()

	if _, err := extractSHA(f.Name(), "missing.tar.gz"); err == nil {
		t.Fatal("expected error")
	}
}

// ── InstalledVersion tests ────────────────────────────────────────────────────

func writeFakeBinary(t *testing.T, dir, version string) string {
	t.Helper()
	path := filepath.Join(dir, BinaryName)
	content := fmt.Sprintf("#!/bin/sh\necho 'shipyard-crew %s (test, built 2026-04-20)'\n", version)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstalledVersion_happyPath(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "0.1.0")

	inst := &Installer{BinDir: dir}
	got, err := inst.InstalledVersion()
	if err != nil {
		t.Fatalf("InstalledVersion: %v", err)
	}
	if got != "0.1.0" {
		t.Errorf("want 0.1.0, got %q", got)
	}
}

func TestInstalledVersion_absent_returnsErrNotInstalled(t *testing.T) {
	inst := &Installer{BinDir: t.TempDir()}
	_, err := inst.InstalledVersion()
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("want ErrNotInstalled, got %v", err)
	}
}

// ── Install end-to-end tests ──────────────────────────────────────────────────

func TestInstall_happyPath(t *testing.T) {
	client := buildHappyClient(t, "0.1.0", Platform{"linux", "amd64"})
	inst := newTestInstaller(t, client)

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	info, err := os.Stat(inst.BinPath())
	if err != nil {
		t.Fatalf("binary missing: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("binary not executable: %v", info.Mode())
	}

	// State/run dirs must exist with 0700 perms.
	sinfo, err := os.Stat(inst.StateDir)
	if err != nil {
		t.Fatalf("StateDir missing: %v", err)
	}
	if sinfo.Mode().Perm() != 0o700 {
		t.Errorf("StateDir perms %v, want 0700", sinfo.Mode().Perm())
	}
	rinfo, err := os.Stat(inst.RunDir)
	if err != nil {
		t.Fatalf("RunDir missing: %v", err)
	}
	if rinfo.Mode().Perm() != 0o700 {
		t.Errorf("RunDir perms %v, want 0700", rinfo.Mode().Perm())
	}
}

func TestInstall_checksumMismatch_rollsBack(t *testing.T) {
	content := []byte("fake")
	archive, _ := makeTarGz(t, content)
	artifact := ArtifactName("0.1.0", Platform{"linux", "amd64"})
	base := "https://fake.example/releases/download"
	wrong := strings.Repeat("0", 64)

	client := &fakeHTTPClient{
		responses: map[string]func() *http.Response{
			base + "/" + ReleaseTag("0.1.0") + "/" + artifact:                      stockResponse(200, archive),
			base + "/" + ReleaseTag("0.1.0") + "/" + ChecksumManifestName("0.1.0"): stockResponse(200, []byte(wrong+"  "+artifact+"\n")),
		},
	}
	inst := newTestInstaller(t, client)

	err := inst.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum error, got %v", err)
	}
	if _, err := os.Stat(inst.BinPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("binary should not exist after checksum failure")
	}
}

func TestInstall_alreadyInstalledSameVersion_returnsErrAlreadyInstalled(t *testing.T) {
	dir := t.TempDir()
	inst := &Installer{
		Version:     "0.1.0",
		Platform:    Platform{"linux", "amd64"},
		BinDir:      dir,
		HTTPClient:  &fakeHTTPClient{},
		ReleaseBase: "https://fake.example/releases/download",
		Warn:        io.Discard,
	}
	writeFakeBinary(t, dir, "0.1.0")

	err := inst.Install(context.Background())
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("want ErrAlreadyInstalled, got %v", err)
	}
}

func TestInstall_differentVersion_noForce_returnsErrUpgradeRequired(t *testing.T) {
	dir := t.TempDir()
	inst := &Installer{
		Version:     "0.2.0",
		Platform:    Platform{"linux", "amd64"},
		BinDir:      dir,
		HTTPClient:  &fakeHTTPClient{},
		ReleaseBase: "https://fake.example/releases/download",
		Warn:        io.Discard,
	}
	writeFakeBinary(t, dir, "0.1.0")

	err := inst.Install(context.Background())
	if !errors.Is(err, ErrUpgradeRequired) {
		t.Errorf("want ErrUpgradeRequired, got %v", err)
	}
}

func TestInstall_force_reinstallsEvenIfPresent(t *testing.T) {
	client := buildHappyClient(t, "0.1.0", Platform{"linux", "amd64"})
	inst := newTestInstaller(t, client)
	inst.Force = true
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeBinary(t, inst.BinDir, "0.1.0")

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install with Force: %v", err)
	}
	// Binary must exist after force reinstall.
	if _, err := os.Stat(inst.BinPath()); err != nil {
		t.Errorf("binary missing after force: %v", err)
	}
}

func TestInstall_createsBinDirIfMissing(t *testing.T) {
	client := buildHappyClient(t, "0.1.0", Platform{"linux", "amd64"})
	inst := newTestInstaller(t, client)
	// Override BinDir to a nested, absent directory.
	inst.BinDir = filepath.Join(t.TempDir(), "deeper", "bin")

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(inst.BinDir); err != nil {
		t.Errorf("BinDir not created: %v", err)
	}
}

func TestInstall_atomicRename_tempCleanedUpOnFail(t *testing.T) {
	client := buildHappyClient(t, "0.1.0", Platform{"linux", "amd64"})
	inst := newTestInstaller(t, client)
	inst.rename = func(old, new string) error { return errors.New("rename failed") }

	if err := inst.Install(context.Background()); err == nil {
		t.Fatal("expected rename error")
	}
	// Destination must not exist.
	if _, err := os.Stat(inst.BinPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("dest should not exist after rename failure")
	}
	// Temp must be cleaned up.
	matches, _ := filepath.Glob(filepath.Join(inst.BinDir, ".shipyard-crew-*"))
	if len(matches) != 0 {
		t.Errorf("leaked temp files: %v", matches)
	}
}

func TestInstall_unsupportedPlatform_failsFast(t *testing.T) {
	inst := newTestInstaller(t, &fakeHTTPClient{})
	inst.Platform = Platform{OS: "windows", Arch: "amd64"}

	err := inst.Install(context.Background())
	if !errors.Is(err, ErrPlatformUnsupported) {
		t.Errorf("want ErrPlatformUnsupported, got %v", err)
	}
	// No binary should be installed.
	if _, err := os.Stat(inst.BinPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("binary must not be created on platform error")
	}
}

func TestInstall_404onArtifact_errorsWithContext(t *testing.T) {
	client := &fakeHTTPClient{}
	inst := newTestInstaller(t, client)

	err := inst.Install(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "download") {
		t.Errorf("error should mention download, got %v", err)
	}
}

// ── Uninstall tests ───────────────────────────────────────────────────────────

func TestUninstall_removesBinary_preservesState(t *testing.T) {
	inst := newTestInstaller(t, &fakeHTTPClient{})
	if err := os.MkdirAll(inst.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.BinPath(), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(inst.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stateMarker := filepath.Join(inst.StateDir, "my-agent", "agent.yaml")
	if err := os.MkdirAll(filepath.Dir(stateMarker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateMarker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if _, err := os.Stat(inst.BinPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("binary still present after uninstall")
	}
	if _, err := os.Stat(stateMarker); err != nil {
		t.Errorf("state must be preserved, got %v", err)
	}
}

func TestUninstall_binaryAbsent_noError(t *testing.T) {
	inst := newTestInstaller(t, &fakeHTTPClient{})
	if err := inst.Uninstall(context.Background()); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestInstall_warnsWhenBinDirNotInPATH(t *testing.T) {
	client := buildHappyClient(t, "0.1.0", Platform{"linux", "amd64"})
	inst := newTestInstaller(t, client)
	var warn bytes.Buffer
	inst.Warn = &warn

	t.Setenv("PATH", "/usr/bin:/bin")

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(warn.String(), inst.BinDir) {
		t.Errorf("expected PATH warning, got %q", warn.String())
	}
}

func TestInstall_silentWhenBinDirInPATH(t *testing.T) {
	client := buildHappyClient(t, "0.1.0", Platform{"linux", "amd64"})
	inst := newTestInstaller(t, client)
	var warn bytes.Buffer
	inst.Warn = &warn

	t.Setenv("PATH", "/usr/bin:"+inst.BinDir+":/bin")

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if warn.Len() != 0 {
		t.Errorf("expected no warning, got %q", warn.String())
	}
}
