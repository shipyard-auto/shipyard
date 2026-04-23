package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// newCLIBackendWithHome returns a CLIBackend pinned to homeDir for
// ~/.claude.json lookups and using selfPath as the synthesised server
// command. Keeps each test hermetic.
func newCLIBackendWithHome(t *testing.T, homeDir, selfPath string) *CLIBackend {
	t.Helper()
	return NewCLIBackend().
		WithSelfPath(selfPath).
		WithUserHomeDir(func() (string, error) { return homeDir, nil })
}

func readMCPConfig(t *testing.T, path string) map[string]map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var body map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return body
}

func TestBuildMCPConfig_NoToolsNoMCP(t *testing.T) {
	b := newCLIBackendWithHome(t, t.TempDir(), "/usr/local/bin/shipyard-crew")
	agent := &crew.Agent{Name: "a"}
	path, cleanup, err := b.buildMCPConfig(agent)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	defer cleanup()
	if path != "" {
		t.Fatalf("want empty path when agent has no tools or mcps, got %q", path)
	}
}

func TestBuildMCPConfig_InternalServerOnly(t *testing.T) {
	selfPath := "/usr/local/bin/shipyard-crew"
	b := newCLIBackendWithHome(t, t.TempDir(), selfPath)
	agent := &crew.Agent{
		Name: "greeter",
		Tools: []crew.Tool{
			{Name: "echo", Protocol: crew.ToolExec, Command: []string{"/bin/true"}},
		},
	}
	path, cleanup, err := b.buildMCPConfig(agent)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	defer cleanup()
	if path == "" {
		t.Fatalf("want non-empty path")
	}
	body := readMCPConfig(t, path)
	servers := body["mcpServers"]
	if len(servers) != 1 {
		t.Fatalf("want 1 server, got %d", len(servers))
	}
	raw, ok := servers[internalServerKey]
	if !ok {
		t.Fatalf("internal server missing; keys=%v", servers)
	}
	var def map[string]any
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatalf("parse internal: %v", err)
	}
	if def["type"] != "stdio" {
		t.Fatalf("type=%v", def["type"])
	}
	if def["command"] != selfPath {
		t.Fatalf("command=%v want %s", def["command"], selfPath)
	}
	args := def["args"].([]any)
	wantArgs := []any{"mcp-serve", "--agent", "greeter"}
	if len(args) != len(wantArgs) {
		t.Fatalf("args=%v", args)
	}
	for i := range args {
		if args[i] != wantArgs[i] {
			t.Fatalf("args[%d]=%v want %v", i, args[i], wantArgs[i])
		}
	}
}

func TestBuildMCPConfig_ExternalRefCopiedVerbatim(t *testing.T) {
	home := t.TempDir()
	// Write a realistic ~/.claude.json shape.
	claude := `{
		"mcpServers": {
			"chrome-devtools": {"type":"stdio","command":"npx","args":["-y","chrome-devtools-mcp"],"env":{"FOO":"BAR"}}
		}
	}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claude), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newCLIBackendWithHome(t, home, "/bin/self")
	agent := &crew.Agent{
		Name:       "greeter",
		MCPServers: []crew.MCPServerRef{{Ref: "chrome-devtools"}},
	}
	path, cleanup, err := b.buildMCPConfig(agent)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	defer cleanup()

	body := readMCPConfig(t, path)
	servers := body["mcpServers"]
	if _, ok := servers[internalServerKey]; ok {
		t.Fatalf("internal key should be absent when no tools declared")
	}
	raw, ok := servers["chrome-devtools"]
	if !ok {
		t.Fatalf("chrome-devtools missing; keys=%v", servers)
	}
	// Byte-for-byte equivalence (after re-encoding) with source.
	var got, want map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(`{"type":"stdio","command":"npx","args":["-y","chrome-devtools-mcp"],"env":{"FOO":"BAR"}}`), &want)
	if got["command"] != want["command"] {
		t.Fatalf("command lost: %v vs %v", got, want)
	}
	env, _ := got["env"].(map[string]any)
	if env == nil || env["FOO"] != "BAR" {
		t.Fatalf("env not preserved: %v", got["env"])
	}
}

func TestBuildMCPConfig_BothInternalAndExternal(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"),
		[]byte(`{"mcpServers":{"playwright":{"command":"npx","args":["-y","@playwright/mcp"]}}}`),
		0o600); err != nil {
		t.Fatal(err)
	}
	b := newCLIBackendWithHome(t, home, "/bin/self")
	agent := &crew.Agent{
		Name:       "a",
		Tools:      []crew.Tool{{Name: "echo", Protocol: crew.ToolExec, Command: []string{"/bin/true"}}},
		MCPServers: []crew.MCPServerRef{{Ref: "playwright"}},
	}
	path, cleanup, err := b.buildMCPConfig(agent)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	defer cleanup()
	body := readMCPConfig(t, path)
	servers := body["mcpServers"]
	if len(servers) != 2 {
		t.Fatalf("want 2 servers, got %d: %v", len(servers), servers)
	}
	if _, ok := servers[internalServerKey]; !ok {
		t.Fatalf("internal missing")
	}
	if _, ok := servers["playwright"]; !ok {
		t.Fatalf("playwright missing")
	}
}

func TestBuildMCPConfig_MissingRefHardErrors(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"),
		[]byte(`{"mcpServers":{"playwright":{"command":"npx"}}}`),
		0o600); err != nil {
		t.Fatal(err)
	}
	b := newCLIBackendWithHome(t, home, "/bin/self")
	agent := &crew.Agent{
		Name:       "a",
		MCPServers: []crew.MCPServerRef{{Ref: "ghost"}},
	}
	_, cleanup, err := b.buildMCPConfig(agent)
	defer cleanup()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "playwright") {
		t.Fatalf("error should mention ref and available list: %v", err)
	}
}

func TestBuildMCPConfig_ClaudeConfigMissingButMCPDeclared(t *testing.T) {
	// ~/.claude.json absent, but the agent asks for an external ref. We
	// expect a specific error listing "<none>" so the user knows the source
	// of the problem.
	home := t.TempDir() // empty
	b := newCLIBackendWithHome(t, home, "/bin/self")
	agent := &crew.Agent{
		Name:       "a",
		MCPServers: []crew.MCPServerRef{{Ref: "x"}},
	}
	_, cleanup, err := b.buildMCPConfig(agent)
	defer cleanup()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "<none>") {
		t.Fatalf("want <none> in message, got %v", err)
	}
}

func TestBuildMCPConfig_CleanupRemovesTempFile(t *testing.T) {
	b := newCLIBackendWithHome(t, t.TempDir(), "/bin/self")
	agent := &crew.Agent{
		Name:  "a",
		Tools: []crew.Tool{{Name: "echo", Protocol: crew.ToolExec, Command: []string{"/bin/true"}}},
	}
	path, cleanup, err := b.buildMCPConfig(agent)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("temp file should exist: %v", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove file, got %v", err)
	}
	// Second call must be safe (idempotent).
	cleanup()
}

func TestBuildMCPConfig_SelfPathFallsBackToExecutable(t *testing.T) {
	// Without WithSelfPath, the backend must call the injected executable
	// resolver. Confirm that our override takes effect by reading back the
	// `command` field.
	b := NewCLIBackend().
		WithUserHomeDir(func() (string, error) { return t.TempDir(), nil })
	b.executable = func() (string, error) { return "/injected/crew", nil }

	agent := &crew.Agent{
		Name:  "a",
		Tools: []crew.Tool{{Name: "echo", Protocol: crew.ToolExec, Command: []string{"/bin/true"}}},
	}
	path, cleanup, err := b.buildMCPConfig(agent)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	defer cleanup()
	body := readMCPConfig(t, path)
	var def map[string]any
	_ = json.Unmarshal(body["mcpServers"][internalServerKey], &def)
	if def["command"] != "/injected/crew" {
		t.Fatalf("executable resolver not used: %v", def["command"])
	}
}
