package crew

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type scaffoldedYAML struct {
	SchemaVersion string `yaml:"schema_version"`
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	Backend       struct {
		Type    string   `yaml:"type"`
		Command []string `yaml:"command"`
		Model   string   `yaml:"model"`
	} `yaml:"backend"`
	Execution struct {
		Mode string `yaml:"mode"`
		Pool string `yaml:"pool"`
	} `yaml:"execution"`
	Conversation struct {
		Mode string `yaml:"mode"`
		Key  string `yaml:"key"`
	} `yaml:"conversation"`
	Triggers []any `yaml:"triggers"`
	Tools    []any `yaml:"tools"`
}

func hireSetup(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("SHIPYARD_HOME", tmp)
	return tmp
}

func TestRunHire_CreatesStructure(t *testing.T) {
	home := hireSetup(t)
	var out bytes.Buffer
	if err := runHire(&out, "promo-hunter", hireFlags{backend: "cli", mode: "on-demand"}); err != nil {
		t.Fatalf("runHire: %v", err)
	}

	dir := filepath.Join(home, "crew", "promo-hunter")
	for _, rel := range []string{"agent.yaml", "prompt.md", "memory/.gitkeep"} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if info.IsDir() {
			t.Fatalf("%s should be a file", rel)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s perm = %o, want 0600", rel, perm)
		}
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir perm = %o, want 0700", perm)
	}
	memInfo, err := os.Stat(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatalf("stat memory: %v", err)
	}
	if perm := memInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("memory perm = %o, want 0700", perm)
	}

	msg := out.String()
	if !strings.Contains(msg, "Crew member \"promo-hunter\" created") {
		t.Fatalf("missing creation message: %q", msg)
	}
	if !strings.Contains(msg, "Next steps:") {
		t.Fatalf("missing next steps: %q", msg)
	}
	if !strings.Contains(msg, "shipyard crew run promo-hunter") {
		t.Fatalf("missing run hint: %q", msg)
	}

	// Scaffolded cli backend must wire prompt.md via {{.Prompt}} so the
	// agent's identity is actually delivered to the subprocess. Without the
	// placeholder, cli.go rejects the run at turn time.
	raw, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatalf("read agent.yaml: %v", err)
	}
	var doc scaffoldedYAML
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal agent.yaml: %v", err)
	}
	if doc.Backend.Type != "cli" {
		t.Fatalf("backend.type = %q want cli", doc.Backend.Type)
	}
	foundPlaceholder := false
	for _, a := range doc.Backend.Command {
		if strings.Contains(a, "{{.Prompt}}") {
			foundPlaceholder = true
			break
		}
	}
	if !foundPlaceholder {
		t.Fatalf("scaffolded cli command must reference {{.Prompt}}, got %v", doc.Backend.Command)
	}
}

func TestRunHire_Validation(t *testing.T) {
	_ = hireSetup(t)

	longName := strings.Repeat("a", 64)
	cases := []struct {
		label string
		name  string
		f     hireFlags
	}{
		{"empty name", "", hireFlags{backend: "cli", mode: "on-demand"}},
		{"uppercase", "BAD", hireFlags{backend: "cli", mode: "on-demand"}},
		{"leading hyphen", "-foo", hireFlags{backend: "cli", mode: "on-demand"}},
		{"too long", longName, hireFlags{backend: "cli", mode: "on-demand"}},
		{"invalid backend", "ok", hireFlags{backend: "nope", mode: "on-demand"}},
		{"invalid mode", "ok", hireFlags{backend: "cli", mode: "nope"}},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			err := runHire(io.Discard, c.name, c.f)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestRunHire_ForceOverwrite(t *testing.T) {
	home := hireSetup(t)

	if err := runHire(io.Discard, "dup", hireFlags{backend: "cli", mode: "on-demand"}); err != nil {
		t.Fatalf("first hire: %v", err)
	}
	marker := filepath.Join(home, "crew", "dup", "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatalf("marker: %v", err)
	}

	if err := runHire(io.Discard, "dup", hireFlags{backend: "cli", mode: "on-demand"}); err == nil {
		t.Fatal("expected error without --force")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker must still exist without force: %v", err)
	}

	if err := runHire(io.Discard, "dup", hireFlags{backend: "cli", mode: "on-demand", force: true}); err != nil {
		t.Fatalf("force hire: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker must be gone after --force, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "crew", "dup", "agent.yaml")); err != nil {
		t.Fatalf("agent.yaml missing after force: %v", err)
	}
}

func TestRunHire_BackendAnthropicAPI(t *testing.T) {
	home := hireSetup(t)

	if err := runHire(io.Discard, "bob", hireFlags{backend: "anthropic_api", mode: "on-demand"}); err != nil {
		t.Fatalf("runHire: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "crew", "bob", "agent.yaml"))
	if err != nil {
		t.Fatalf("read yaml: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "type: anthropic_api") {
		t.Fatalf("missing type: anthropic_api in %q", body)
	}
	if !strings.Contains(body, "model: claude-sonnet-4-6") {
		t.Fatalf("missing model in %q", body)
	}
	if strings.Contains(body, "command:") {
		t.Fatalf("anthropic_api must not set command: %q", body)
	}
}

func TestRunHire_ModeService(t *testing.T) {
	home := hireSetup(t)

	if err := runHire(io.Discard, "svc", hireFlags{backend: "cli", mode: "service"}); err != nil {
		t.Fatalf("runHire: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "crew", "svc", "agent.yaml"))
	if err != nil {
		t.Fatalf("read yaml: %v", err)
	}
	if !strings.Contains(string(raw), "mode: service") {
		t.Fatalf("missing mode: service: %q", string(raw))
	}
}

func TestRunHire_FromFlagIsNoop(t *testing.T) {
	home := hireSetup(t)

	if err := runHire(io.Discard, "fromx", hireFlags{backend: "cli", mode: "on-demand", from: "ignored-preset"}); err != nil {
		t.Fatalf("runHire: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "crew", "fromx", "agent.yaml")); err != nil {
		t.Fatalf("agent.yaml missing: %v", err)
	}
}

func TestRunHire_GeneratedYAMLParses(t *testing.T) {
	home := hireSetup(t)

	cases := []struct {
		label   string
		flags   hireFlags
		wantCmd bool
	}{
		{"cli backend", hireFlags{backend: "cli", mode: "on-demand"}, true},
		{"anthropic backend service", hireFlags{backend: "anthropic_api", mode: "service"}, false},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			name := "parse-" + strings.ReplaceAll(c.label, " ", "-")
			if err := runHire(io.Discard, name, c.flags); err != nil {
				t.Fatalf("runHire: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(home, "crew", name, "agent.yaml"))
			if err != nil {
				t.Fatalf("read yaml: %v", err)
			}
			var doc scaffoldedYAML
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("unmarshal: %v\n---\n%s", err, string(raw))
			}
			if doc.SchemaVersion != agentSchemaVersion {
				t.Errorf("schema_version = %q, want %q", doc.SchemaVersion, agentSchemaVersion)
			}
			if doc.Name != name {
				t.Errorf("name = %q, want %q", doc.Name, name)
			}
			if doc.Backend.Type != c.flags.backend {
				t.Errorf("backend.type = %q, want %q", doc.Backend.Type, c.flags.backend)
			}
			if c.wantCmd {
				if len(doc.Backend.Command) == 0 {
					t.Error("cli backend: command missing")
				}
				if doc.Backend.Model != "" {
					t.Errorf("cli backend must not set model, got %q", doc.Backend.Model)
				}
			} else {
				if doc.Backend.Model == "" {
					t.Error("anthropic_api: model missing")
				}
				if len(doc.Backend.Command) > 0 {
					t.Errorf("anthropic_api must not set command, got %v", doc.Backend.Command)
				}
			}
			if doc.Execution.Mode != c.flags.mode {
				t.Errorf("execution.mode = %q, want %q", doc.Execution.Mode, c.flags.mode)
			}
			if doc.Execution.Pool != "cli" {
				t.Errorf("execution.pool = %q, want cli", doc.Execution.Pool)
			}
			if doc.Conversation.Mode != "stateless" {
				t.Errorf("conversation.mode = %q, want stateless", doc.Conversation.Mode)
			}
			if len(doc.Triggers) != 0 || len(doc.Tools) != 0 {
				t.Errorf("triggers/tools should be empty")
			}
		})
	}
}

func TestNewHireCmd_Flags(t *testing.T) {
	cmd := newHireCmd()
	for _, name := range []string{"backend", "mode", "force", "from"} {
		if f := cmd.Flags().Lookup(name); f == nil {
			t.Errorf("flag --%s missing", name)
		}
	}
}

func TestNewHireCmd_CobraExecute(t *testing.T) {
	home := hireSetup(t)
	prev := resolveBinaryFn
	resolveBinaryFn = func() (string, error) { return "/fake/shipyard-crew", nil }
	t.Cleanup(func() { resolveBinaryFn = prev })

	cmd := newHireCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"viacobra", "--backend", "anthropic_api", "--mode", "service"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "crew", "viacobra", "agent.yaml")); err != nil {
		t.Fatalf("agent.yaml missing: %v", err)
	}
}

func TestRenderTemplate_MissingTemplate(t *testing.T) {
	err := renderTemplate("does-not-exist.tmpl", filepath.Join(t.TempDir(), "x"), 0o600, struct{}{})
	if err == nil {
		t.Fatal("expected error for missing template")
	}
	if !strings.Contains(err.Error(), "read template") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRenderTemplate_OpenFailure(t *testing.T) {
	// Destination parent does not exist: os.OpenFile returns error.
	err := renderTemplate("agent.yaml.tmpl", filepath.Join(t.TempDir(), "missing", "nested", "x"), 0o600, struct {
		Name, BackendType, Mode, SchemaVersion string
	}{"a", "cli", "on-demand", "1"})
	if err == nil {
		t.Fatal("expected error for missing parent dir")
	}
}

func TestRunHire_ExistingDirBlockedWithoutForce(t *testing.T) {
	// Covers the branch that returns fmt.Errorf when dir exists and !force,
	// confirming the dedicated error text.
	home := hireSetup(t)
	dir := filepath.Join(home, "crew", "alreadyhere")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	err := runHire(io.Discard, "alreadyhere", hireFlags{backend: "cli", mode: "on-demand"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewHireCmd_GuardTriggersWhenBinaryMissing(t *testing.T) {
	_ = hireSetup(t)
	prev := resolveBinaryFn
	resolveBinaryFn = func() (string, error) { return "", errors.New("no binary") }
	t.Cleanup(func() { resolveBinaryFn = prev })

	cmd := newHireCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"guarded"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected guard error")
	}
	if !errors.Is(err, ErrAddonNotInstalled) {
		t.Fatalf("want ErrAddonNotInstalled, got %v", err)
	}
}
