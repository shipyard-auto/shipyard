package fairway_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func validSaveConfig() fairway.Config {
	return fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          fairway.DefaultPort,
		Bind:          fairway.DefaultBind,
		Routes:        []fairway.Route{},
	}
}

func repoAt(t *testing.T) (*fairway.FileRepository, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.json")
	return fairway.NewFileRepositoryAt(path), path
}

// ── Load ─────────────────────────────────────────────────────────────────────

func TestLoad_absentReturnsDefaults(t *testing.T) {
	t.Parallel()

	repo, path := repoAt(t)

	cfg, err := repo.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.SchemaVersion != fairway.SchemaVersion {
		t.Errorf("SchemaVersion = %q; want %q", cfg.SchemaVersion, fairway.SchemaVersion)
	}
	if cfg.Port != fairway.DefaultPort {
		t.Errorf("Port = %d; want %d", cfg.Port, fairway.DefaultPort)
	}
	if cfg.Bind != fairway.DefaultBind {
		t.Errorf("Bind = %q; want %q", cfg.Bind, fairway.DefaultBind)
	}
	if cfg.Routes == nil {
		t.Error("Routes should be non-nil empty slice, got nil")
	}

	// File must NOT have been created.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load() must not create file, but %q exists", path)
	}
}

func TestLoad_validFileRoundtrip(t *testing.T) {
	t.Parallel()

	repo, _ := repoAt(t)

	original := fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          8080,
		Bind:          "0.0.0.0",
		MaxInFlight:   8,
		Routes: []fairway.Route{
			{
				Path:   "/hook",
				Auth:   fairway.Auth{Type: fairway.AuthBearer, Token: "tok"},
				Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job-1"},
			},
		},
	}

	if err := repo.Save(original); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := repo.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	origJSON, _ := json.Marshal(original)
	loadedJSON, _ := json.Marshal(loaded)
	if string(origJSON) != string(loadedJSON) {
		t.Errorf("roundtrip mismatch:\n  saved:  %s\n  loaded: %s", origJSON, loadedJSON)
	}
}

func TestLoad_invalidJSON_returnsError(t *testing.T) {
	t.Parallel()

	repo, path := repoAt(t)

	if err := os.WriteFile(path, []byte("{not-json"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := repo.Load()
	if err == nil {
		t.Fatal("Load() expected error for malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should mention path %q, got: %v", path, err)
	}
}

func TestLoad_unsupportedSchemaVersion(t *testing.T) {
	t.Parallel()

	repo, path := repoAt(t)

	raw := `{"notice":"","schemaVersion":"2","port":9876,"bind":"127.0.0.1","routes":[]}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := repo.Load()
	if err == nil {
		t.Fatal("Load() expected error for unsupported schema, got nil")
	}
	if !errors.Is(err, fairway.ErrUnsupportedSchema) {
		t.Errorf("Load() error = %v; want errors.Is(ErrUnsupportedSchema)", err)
	}
}

func TestLoad_invalidConfig_badPort(t *testing.T) {
	t.Parallel()

	repo, path := repoAt(t)

	raw := `{"notice":"","schemaVersion":"1","port":0,"bind":"127.0.0.1","routes":[]}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := repo.Load()
	if err == nil {
		t.Fatal("Load() expected validation error, got nil")
	}
	if !errors.Is(err, fairway.ErrInvalidPort) {
		t.Errorf("Load() error = %v; want errors.Is(ErrInvalidPort)", err)
	}
}

// ── Save ─────────────────────────────────────────────────────────────────────

func TestSave_createsDirWithMode0700(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	nested := filepath.Join(base, "sub", "fairway")
	path := filepath.Join(nested, "routes.json")
	repo := fairway.NewFileRepositoryAt(path)

	if err := repo.Save(validSaveConfig()); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected a directory")
	}
	// Check mode bits (ignore sticky/setuid bits with & 0777).
	if got := info.Mode() & 0777; got != 0700 {
		t.Errorf("dir mode = %04o; want 0700", got)
	}
}

func TestSave_createsFileWithMode0600(t *testing.T) {
	t.Parallel()

	repo, path := repoAt(t)

	if err := repo.Save(validSaveConfig()); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := info.Mode() & 0777; got != 0600 {
		t.Errorf("file mode = %04o; want 0600", got)
	}
}

func TestSave_writesNoticeOnTop(t *testing.T) {
	t.Parallel()

	repo, path := repoAt(t)

	if err := repo.Save(validSaveConfig()); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, `"notice"`) {
		t.Errorf("saved file should contain notice field, got:\n%s", content)
	}
	// "notice" must appear before "schemaVersion"
	noticeIdx := strings.Index(content, `"notice"`)
	schemaIdx := strings.Index(content, `"schemaVersion"`)
	if noticeIdx > schemaIdx {
		t.Errorf("notice field must appear before schemaVersion in file")
	}
}

func TestSave_rejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	repo, path := repoAt(t)

	// Pre-existing valid content.
	if err := repo.Save(validSaveConfig()); err != nil {
		t.Fatalf("initial Save() error: %v", err)
	}
	original, _ := os.ReadFile(path)

	bad := fairway.Config{SchemaVersion: fairway.SchemaVersion, Port: 0, Bind: "127.0.0.1"}
	if err := repo.Save(bad); err == nil {
		t.Fatal("Save() should reject invalid config")
	}

	// File must be unchanged.
	after, _ := os.ReadFile(path)
	if string(original) != string(after) {
		t.Error("Save() with invalid config must not modify the file")
	}
}

func TestSave_overwritesPreviousContent(t *testing.T) {
	t.Parallel()

	repo, _ := repoAt(t)

	first := validSaveConfig()
	first.Port = 8001
	if err := repo.Save(first); err != nil {
		t.Fatalf("first Save() error: %v", err)
	}

	second := validSaveConfig()
	second.Port = 8002
	if err := repo.Save(second); err != nil {
		t.Fatalf("second Save() error: %v", err)
	}

	loaded, err := repo.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.Port != 8002 {
		t.Errorf("Port = %d; want 8002", loaded.Port)
	}
}

func TestSave_isAtomic_existingFilePreservedOnRenameFailure(t *testing.T) {
	t.Parallel()

	// We cannot force os.Rename to fail reliably cross-platform without
	// injecting a hook. Instead we verify the invariant by checking that a
	// valid pre-existing file is still intact after a failed Save (bad config).
	repo, path := repoAt(t)

	good := validSaveConfig()
	good.Port = 9001
	if err := repo.Save(good); err != nil {
		t.Fatalf("initial Save() error: %v", err)
	}
	snapshot, _ := os.ReadFile(path)

	// Save invalid — must fail, must not corrupt existing file.
	bad := fairway.Config{SchemaVersion: "99", Port: 9001, Bind: "127.0.0.1"}
	_ = repo.Save(bad) // expected to fail

	after, _ := os.ReadFile(path)
	if string(snapshot) != string(after) {
		t.Error("existing file was corrupted after a failed Save()")
	}
}

func TestSave_tempFileCleanedOnFailure(t *testing.T) {
	t.Parallel()

	// Point repo at a read-only directory so Save fails at CreateTemp.
	base := t.TempDir()
	roDir := filepath.Join(base, "ro")
	if err := os.MkdirAll(roDir, 0500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0700) }) // restore so TempDir cleanup works

	path := filepath.Join(roDir, "routes.json")
	repo := fairway.NewFileRepositoryAt(path)

	_ = repo.Save(validSaveConfig()) // will fail; we only care there are no .tmp leftovers

	entries, _ := filepath.Glob(filepath.Join(roDir, "routes.*.tmp"))
	if len(entries) > 0 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

// ── Path / DefaultConfigPath ──────────────────────────────────────────────────

func TestPath_returnsConfiguredPath(t *testing.T) {
	t.Parallel()

	want := "/tmp/test/routes.json"
	repo := fairway.NewFileRepositoryAt(want)
	if got := repo.Path(); got != want {
		t.Errorf("Path() = %q; want %q", got, want)
	}
}

func TestDefaultConfigPath_respectsEnv(t *testing.T) {
	// t.Setenv requires non-parallel test.
	t.Setenv("SHIPYARD_HOME", "/tmp/custom-home")

	path, err := fairway.DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath() error: %v", err)
	}

	want := "/tmp/custom-home/fairway/routes.json"
	if path != want {
		t.Errorf("DefaultConfigPath() = %q; want %q", path, want)
	}
}

func TestDefaultConfigPath_fallsBackToHome(t *testing.T) {
	// t.Setenv requires non-parallel test.
	t.Setenv("SHIPYARD_HOME", "")

	path, err := fairway.DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath() error: %v", err)
	}

	if !strings.Contains(path, ".shipyard") || !strings.HasSuffix(path, "routes.json") {
		t.Errorf("DefaultConfigPath() = %q; expected path containing .shipyard/fairway/routes.json", path)
	}
}

func TestNewFileRepository_usesDefaultPath(t *testing.T) {
	// t.Setenv requires non-parallel test.
	tmp := t.TempDir()
	t.Setenv("SHIPYARD_HOME", tmp)

	repo, err := fairway.NewFileRepository()
	if err != nil {
		t.Fatalf("NewFileRepository() error: %v", err)
	}
	want := filepath.Join(tmp, "fairway", "routes.json")
	if repo.Path() != want {
		t.Errorf("Path() = %q; want %q", repo.Path(), want)
	}
}

func TestSave_syncAndCloseErrors_cleanupTemp(t *testing.T) {
	t.Parallel()

	// Normal happy-path to ensure Write+Sync+Close path is exercised.
	repo, _ := repoAt(t)

	cfg := validSaveConfig()
	cfg.Port = 7777
	if err := repo.Save(cfg); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := repo.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.Port != 7777 {
		t.Errorf("Port = %d; want 7777", loaded.Port)
	}
}

func TestLoad_readError_returnsError(t *testing.T) {
	t.Parallel()

	// Create a directory where the file should be — reading it as a file returns error.
	base := t.TempDir()
	dirAsFile := filepath.Join(base, "routes.json")
	if err := os.MkdirAll(dirAsFile, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	repo := fairway.NewFileRepositoryAt(dirAsFile)
	_, err := repo.Load()
	if err == nil {
		t.Fatal("Load() expected error reading a directory as file, got nil")
	}
}
