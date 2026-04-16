package fairway

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Run("Load_absentReturnsDefaults", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		repo := NewFileRepositoryAt(path)

		got, err := repo.Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		want := Config{
			SchemaVersion: SchemaVersion,
			Port:          DefaultPort,
			Bind:          DefaultBind,
			Routes:        []Route{},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Load() = %#v, want %#v", got, want)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Stat(%q) error = %v, want not-exist", path, err)
		}
	})

	t.Run("Load_validFileRoundtrip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		repo := NewFileRepositoryAt(path)
		cfg := testConfig()

		if err := repo.Save(cfg); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		got, err := repo.Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !reflect.DeepEqual(got, cfg) {
			t.Fatalf("Load() = %#v, want %#v", got, cfg)
		}
	})

	t.Run("Load_invalidJSON_returnsError", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		_, err := NewFileRepositoryAt(path).Load()
		if err == nil {
			t.Fatal("Load() error = nil, want error")
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("Load() error = %q, want path %q", err.Error(), path)
		}
	})

	t.Run("Load_readErrorReturnsPath", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}

		_, err := NewFileRepositoryAt(path).Load()
		if err == nil {
			t.Fatal("Load() error = nil, want error")
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("Load() error = %q, want path %q", err.Error(), path)
		}
	})

	t.Run("Load_unsupportedSchemaVersion", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		writeRawConfig(t, path, map[string]any{
			"notice":        storeNotice,
			"schemaVersion": "2",
			"port":          DefaultPort,
			"bind":          DefaultBind,
			"routes":        []any{},
		})

		_, err := NewFileRepositoryAt(path).Load()
		if !errors.Is(err, ErrUnsupportedSchema) {
			t.Fatalf("Load() error = %v, want ErrUnsupportedSchema", err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("Load() error = %q, want path %q", err.Error(), path)
		}
		if !strings.Contains(err.Error(), `"2"`) || !strings.Contains(err.Error(), `"1"`) {
			t.Fatalf("Load() error = %q, want found and expected versions", err.Error())
		}
	})

	t.Run("Load_invalidConfig", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		writeRawConfig(t, path, map[string]any{
			"notice":        storeNotice,
			"schemaVersion": SchemaVersion,
			"port":          0,
			"bind":          DefaultBind,
			"routes":        []any{},
		})

		_, err := NewFileRepositoryAt(path).Load()
		if !errors.Is(err, ErrInvalidPort) {
			t.Fatalf("Load() error = %v, want ErrInvalidPort", err)
		}
	})
}

func TestSave(t *testing.T) {
	t.Run("Save_createsDirWithMode0700", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		repo := NewFileRepositoryAt(path)

		if err := repo.Save(testConfig()); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		info, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("Stat(dir) error = %v", err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("dir mode = %o, want 700", got)
		}
	})

	t.Run("Save_createsFileWithMode0600", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		repo := NewFileRepositoryAt(path)

		if err := repo.Save(testConfig()); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(file) error = %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("file mode = %o, want 600", got)
		}
	})

	t.Run("Save_writesNoticeOnTop", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		repo := NewFileRepositoryAt(path)

		if err := repo.Save(testConfig()); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		lines := strings.Split(string(data), "\n")
		if len(lines) < 2 {
			t.Fatalf("saved file too short: %q", string(data))
		}
		if strings.TrimSpace(lines[1]) != `"notice": "`+storeNotice+`",` {
			t.Fatalf("first JSON field = %q, want notice field", strings.TrimSpace(lines[1]))
		}
	})

	t.Run("Save_isAtomic", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		repo := NewFileRepositoryAt(path)
		original := testConfig()

		if err := repo.Save(original); err != nil {
			t.Fatalf("Save(original) error = %v", err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(before) error = %v", err)
		}

		repo.beforeRename = func(tempPath, finalPath string) error {
			return errors.New("boom before rename")
		}
		updated := testConfig()
		updated.Port = 9999

		err = repo.Save(updated)
		if err == nil {
			t.Fatal("Save(updated) error = nil, want error")
		}

		after, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("ReadFile(after) error = %v", readErr)
		}
		if string(after) != string(before) {
			t.Fatalf("final file changed after failed Save()\n before: %s\n after: %s", string(before), string(after))
		}
	})

	t.Run("Save_rejectsInvalidConfig", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		repo := NewFileRepositoryAt(path)
		valid := testConfig()

		if err := repo.Save(valid); err != nil {
			t.Fatalf("Save(valid) error = %v", err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(before) error = %v", err)
		}

		invalid := valid
		invalid.Port = 0
		err = repo.Save(invalid)
		if !errors.Is(err, ErrInvalidPort) {
			t.Fatalf("Save(invalid) error = %v, want ErrInvalidPort", err)
		}

		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(after) error = %v", err)
		}
		if string(after) != string(before) {
			t.Fatal("file changed after invalid Save()")
		}
	})

	t.Run("Save_overwritesPreviousContent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		repo := NewFileRepositoryAt(path)

		first := testConfig()
		second := testConfig()
		second.Bind = "::1"
		second.Routes = append(second.Routes, Route{
			Path:   "/hooks/telegram",
			Auth:   Auth{Type: AuthLocalOnly},
			Action: Action{Type: ActionTelegramHandle},
		})

		if err := repo.Save(first); err != nil {
			t.Fatalf("Save(first) error = %v", err)
		}
		if err := repo.Save(second); err != nil {
			t.Fatalf("Save(second) error = %v", err)
		}

		got, err := repo.Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !reflect.DeepEqual(got, second) {
			t.Fatalf("Load() = %#v, want %#v", got, second)
		}
	})

	t.Run("Save_tempFileCleanedOnFailure", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		repo := NewFileRepositoryAt(path)
		repo.beforeRename = func(tempPath, finalPath string) error {
			return errors.New("fail before rename")
		}

		err := repo.Save(testConfig())
		if err == nil {
			t.Fatal("Save() error = nil, want error")
		}

		matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(path), "routes.*.tmp"))
		if globErr != nil {
			t.Fatalf("Glob() error = %v", globErr)
		}
		if len(matches) != 0 {
			t.Fatalf("temporary files left behind: %v", matches)
		}
	})

	t.Run("Save_renameFailureCleansTemp", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fairway", "routes.json")
		repo := NewFileRepositoryAt(path)
		repo.beforeRename = func(tempPath, finalPath string) error {
			return os.Remove(tempPath)
		}

		err := repo.Save(testConfig())
		if err == nil {
			t.Fatal("Save() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "rename temp file") {
			t.Fatalf("Save() error = %q, want rename failure", err.Error())
		}

		matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(path), "routes.*.tmp"))
		if globErr != nil {
			t.Fatalf("Glob() error = %v", globErr)
		}
		if len(matches) != 0 {
			t.Fatalf("temporary files left behind: %v", matches)
		}
	})

	t.Run("Save_mkdirAllError", func(t *testing.T) {
		tempDir := t.TempDir()
		blocker := filepath.Join(tempDir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		repo := NewFileRepositoryAt(filepath.Join(blocker, "routes.json"))
		err := repo.Save(testConfig())
		if err == nil {
			t.Fatal("Save() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "create config dir") {
			t.Fatalf("Save() error = %q, want create config dir failure", err.Error())
		}
	})
}

func TestPath(t *testing.T) {
	t.Run("NewFileRepository_usesDefaultPath", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("SHIPYARD_HOME", tempHome)

		repo, err := NewFileRepository()
		if err != nil {
			t.Fatalf("NewFileRepository() error = %v", err)
		}

		want := filepath.Join(tempHome, "fairway", "routes.json")
		if repo.Path() != want {
			t.Fatalf("repo.Path() = %q, want %q", repo.Path(), want)
		}
	})

	t.Run("DefaultConfigPath_respectsEnv", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("SHIPYARD_HOME", tempHome)

		path, err := DefaultConfigPath()
		if err != nil {
			t.Fatalf("DefaultConfigPath() error = %v", err)
		}

		want := filepath.Join(tempHome, "fairway", "routes.json")
		if path != want {
			t.Fatalf("DefaultConfigPath() = %q, want %q", path, want)
		}
	})

	t.Run("DefaultConfigPath_fallsBackToUserHome", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("SHIPYARD_HOME", "")
		t.Setenv("HOME", tempHome)

		path, err := DefaultConfigPath()
		if err != nil {
			t.Fatalf("DefaultConfigPath() error = %v", err)
		}

		want := filepath.Join(tempHome, ".shipyard", "fairway", "routes.json")
		if path != want {
			t.Fatalf("DefaultConfigPath() = %q, want %q", path, want)
		}
	})

	t.Run("Path_returnsAbsolutePath", func(t *testing.T) {
		repo := NewFileRepositoryAt(filepath.Join("testdata", "routes.json"))
		if !filepath.IsAbs(repo.Path()) {
			t.Fatalf("Path() = %q, want absolute path", repo.Path())
		}
	})
}

func testConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Port:          DefaultPort,
		Bind:          DefaultBind,
		MaxInFlight:   DefaultMaxInFlight,
		Routes: []Route{
			{
				Path:    "/hooks/github",
				Timeout: 30 * time.Second,
				Auth:    Auth{Type: AuthBearer, Token: "secret"},
				Action:  Action{Type: ActionCronRun, Target: "job-1"},
			},
		},
	}
}

func writeRawConfig(t *testing.T, path string, value any) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
