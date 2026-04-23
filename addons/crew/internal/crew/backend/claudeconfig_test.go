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
