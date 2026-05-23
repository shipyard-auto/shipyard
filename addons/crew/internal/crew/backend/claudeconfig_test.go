package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

func homeInTempDir(t *testing.T) (string, func() (string, error)) {
	t.Helper()
	home := t.TempDir()
	return home, func() (string, error) { return home, nil }
}

func writeClaudeConfig(t *testing.T, home, body string) {
	t.Helper()
	path := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
}

func TestLoadClaudeMCPs_Missing(t *testing.T) {
	_, get := homeInTempDir(t)
	got, err := LoadClaudeMCPs(get)
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

func TestLoadClaudeMCPs_Present(t *testing.T) {
	home, get := homeInTempDir(t)
	writeClaudeConfig(t, home, `{
		"mcpServers": {
			"chrome-devtools": {"type":"stdio","command":"npx","args":["-y","chrome-devtools-mcp"]},
			"playwright":      {"type":"stdio","command":"npx","args":["-y","@playwright/mcp"]}
		},
		"unrelated": 42
	}`)
	got, err := LoadClaudeMCPs(get)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 servers, got %d", len(got))
	}
	if _, ok := got["chrome-devtools"]; !ok {
		t.Fatalf("chrome-devtools missing")
	}
	// Make sure the raw message is preserved — we must be able to re-marshal it.
	var def map[string]any
	if err := json.Unmarshal(got["chrome-devtools"], &def); err != nil {
		t.Fatalf("raw message not valid JSON: %v", err)
	}
	if def["command"] != "npx" {
		t.Fatalf("passthrough lost fields: %v", def)
	}
}

func TestLoadClaudeMCPs_Malformed(t *testing.T) {
	home, get := homeInTempDir(t)
	writeClaudeConfig(t, home, `{not json`)
	_, err := LoadClaudeMCPs(get)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("want parse error, got %v", err)
	}
}

func TestLoadClaudeMCPs_NoMCPServersField(t *testing.T) {
	home, get := homeInTempDir(t)
	writeClaudeConfig(t, home, `{"other":123}`)
	got, err := LoadClaudeMCPs(get)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got != nil {
		t.Fatalf("want nil mcpServers, got %v", got)
	}
}

func TestResolveServerRefs_Empty(t *testing.T) {
	got, err := ResolveServerRefs(nil, map[string]json.RawMessage{"x": []byte("{}")})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

func TestResolveServerRefs_Success(t *testing.T) {
	src := map[string]json.RawMessage{
		"chrome-devtools": []byte(`{"command":"npx"}`),
		"playwright":      []byte(`{"command":"npx"}`),
	}
	got, err := ResolveServerRefs(
		[]crew.MCPServerRef{{Ref: "chrome-devtools"}},
		src,
	)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(got) != 1 || string(got["chrome-devtools"]) != `{"command":"npx"}` {
		t.Fatalf("unexpected: %v", got)
	}
}

func TestResolveServerRefs_MissingListsAvailable(t *testing.T) {
	src := map[string]json.RawMessage{
		"chrome-devtools": []byte(`{}`),
		"playwright":      []byte(`{}`),
	}
	_, err := ResolveServerRefs(
		[]crew.MCPServerRef{{Ref: "ghost"}},
		src,
	)
	if err == nil {
		t.Fatalf("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"ghost"`) {
		t.Fatalf("missing ref not named: %v", msg)
	}
	if !strings.Contains(msg, "chrome-devtools") || !strings.Contains(msg, "playwright") {
		t.Fatalf("available keys missing: %v", msg)
	}
}

func TestResolveServerRefs_EmptySourceShowsNone(t *testing.T) {
	_, err := ResolveServerRefs(
		[]crew.MCPServerRef{{Ref: "any"}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "<none>") {
		t.Fatalf("want <none> marker, got %v", err)
	}
}

// TestLoadClaudeMCPsForScope_emptyScope_returnsRoot guards the legacy
// path: callers passing "" must see the root mcpServers map exactly as
// LoadClaudeMCPs has always returned. Critical for backward compatibility
// — every agent without project_scope set continues to behave as before.
func TestLoadClaudeMCPsForScope_emptyScope_returnsRoot(t *testing.T) {
	home, get := homeInTempDir(t)
	writeClaudeConfig(t, home, `{
		"mcpServers": {
			"chrome-devtools": {"command":"npx"}
		},
		"projects": {
			"/Users/leo/foo": {
				"mcpServers": { "github": {"command":"npx"} }
			}
		}
	}`)
	got, err := LoadClaudeMCPsForScope(get, "")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 root server, got %d (project entries leaked into root scope)", len(got))
	}
	if _, ok := got["chrome-devtools"]; !ok {
		t.Fatal("chrome-devtools missing from root scope")
	}
	if _, leaked := got["github"]; leaked {
		t.Fatal("project-scoped 'github' leaked into empty-scope result")
	}
}

// TestLoadClaudeMCPsForScope_projectMergedOnTop asserts that entries in
// projects.<scope>.mcpServers are exposed alongside root entries. Root
// entries that don't clash continue to be visible — projects extend, not
// replace, the global set.
func TestLoadClaudeMCPsForScope_projectMergedOnTop(t *testing.T) {
	home, get := homeInTempDir(t)
	writeClaudeConfig(t, home, `{
		"mcpServers": {
			"chrome-devtools": {"command":"npx","args":["root"]}
		},
		"projects": {
			"/Users/leo/foo": {
				"mcpServers": { "github": {"command":"npx","args":["proj"]} }
			}
		}
	}`)
	got, err := LoadClaudeMCPsForScope(get, "/Users/leo/foo")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 merged servers, got %d: %v", len(got), keys(got))
	}
	if _, ok := got["chrome-devtools"]; !ok {
		t.Error("root chrome-devtools missing after project merge")
	}
	if _, ok := got["github"]; !ok {
		t.Error("project-scoped github missing")
	}
}

// TestLoadClaudeMCPsForScope_projectOverridesRoot guards the precedence
// rule: when the same key exists in both root and project scope, the
// project version wins. This matches how Claude Code itself layers
// project config on top of root.
func TestLoadClaudeMCPsForScope_projectOverridesRoot(t *testing.T) {
	home, get := homeInTempDir(t)
	writeClaudeConfig(t, home, `{
		"mcpServers": {
			"github": {"command":"npx","args":["root-version"]}
		},
		"projects": {
			"/Users/leo/foo": {
				"mcpServers": { "github": {"command":"npx","args":["project-version"]} }
			}
		}
	}`)
	got, err := LoadClaudeMCPsForScope(get, "/Users/leo/foo")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	def := string(got["github"])
	if !strings.Contains(def, "project-version") {
		t.Fatalf("project entry did not override root; got: %s", def)
	}
	if strings.Contains(def, "root-version") {
		t.Fatalf("root entry still visible after project override; got: %s", def)
	}
}

// TestLoadClaudeMCPsForScope_unknownScope_fallsBackToRoot asserts that
// declaring a project_scope that doesn't (yet) have an entry under
// `projects` is not an error — root entries are still served. This
// matches the realistic case "I just hired the agent; I'll run
// `claude mcp add` inside the project later." If a declared ref then
// turns out to be missing, ResolveServerRefs surfaces a clear error
// listing available keys.
func TestLoadClaudeMCPsForScope_unknownScope_fallsBackToRoot(t *testing.T) {
	home, get := homeInTempDir(t)
	writeClaudeConfig(t, home, `{
		"mcpServers": {
			"chrome-devtools": {"command":"npx"}
		},
		"projects": {}
	}`)
	got, err := LoadClaudeMCPsForScope(get, "/Users/leo/nonexistent")
	if err != nil {
		t.Fatalf("unknown scope must not error: %v", err)
	}
	if _, ok := got["chrome-devtools"]; !ok {
		t.Fatal("root entries lost when scope is unknown")
	}
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
