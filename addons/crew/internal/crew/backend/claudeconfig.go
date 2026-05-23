package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// claudeConfigFile is the canonical location where `claude mcp add` writes
// user-level MCP server definitions. Project-scoped entries (under
// projects.<path>.mcpServers) are layered on top via
// LoadClaudeMCPsForScope when the agent declares `project_scope`.
const claudeConfigFile = ".claude.json"

// LoadClaudeMCPs reads the root-level mcpServers map from ~/.claude.json.
// Equivalent to LoadClaudeMCPsForScope(userHome, "") — kept as a stable
// entry point for callers that don't care about project scoping.
//
// A missing file is not an error — it returns (nil, nil), matching the
// common case of a user who has never run `claude mcp add`. A present but
// malformed file IS an error: we prefer to fail loud rather than silently
// lose servers the user believes are configured.
//
// userHome is injected so tests can point at a tempdir. nil means use
// os.UserHomeDir.
func LoadClaudeMCPs(userHome func() (string, error)) (map[string]json.RawMessage, error) {
	return LoadClaudeMCPsForScope(userHome, "")
}

// LoadClaudeMCPsForScope reads the root-level mcpServers map from
// ~/.claude.json and, when projectScope is non-empty, merges the
// per-project map at `projects.<projectScope>.mcpServers` on top. Project
// entries override root entries on key clash — matching how Claude Code
// itself resolves MCP servers when a project is active.
//
// Empty projectScope returns root-only (legacy behavior). When projectScope
// is set but the path has no entry in `projects`, returns the root map
// unchanged — the agent presumably knows the scope exists; if any of its
// declared refs are missing later, ResolveServerRefs surfaces a clear
// error listing the available keys.
//
// Each entry's raw JSON is preserved byte-for-byte so the resulting map
// can be re-marshaled into --mcp-config without interpreting fields the
// spec may extend.
func LoadClaudeMCPsForScope(userHome func() (string, error), projectScope string) (map[string]json.RawMessage, error) {
	if userHome == nil {
		userHome = os.UserHomeDir
	}
	home, err := userHome()
	if err != nil {
		return nil, fmt.Errorf("claude config: home dir: %w", err)
	}
	path := filepath.Join(home, claudeConfigFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("claude config: read %s: %w", path, err)
	}

	var shell struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(raw, &shell); err != nil {
		return nil, fmt.Errorf("claude config: parse %s: %w", path, err)
	}

	if projectScope == "" {
		return shell.MCPServers, nil
	}

	projectEntries := shell.Projects[projectScope].MCPServers
	if len(projectEntries) == 0 {
		// Scope set but project has no MCPs configured — fall back to root.
		// This is not an error: the user may have declared the scope ahead
		// of populating it via `claude mcp add` inside the project.
		return shell.MCPServers, nil
	}

	// Merge: start from root, then let project entries override.
	merged := make(map[string]json.RawMessage, len(shell.MCPServers)+len(projectEntries))
	for k, v := range shell.MCPServers {
		merged[k] = v
	}
	for k, v := range projectEntries {
		merged[k] = v
	}
	return merged, nil
}

// ResolveServerRefs translates agent-level mcp_servers[] references into
// the subset of ~/.claude.json's mcpServers map that the agent is allowed
// to see. Refs that do not resolve produce a hard error naming every
// available key, so the user can spot typos and stale references
// immediately. A nil or empty refs slice returns (nil, nil).
func ResolveServerRefs(refs []crew.MCPServerRef, src map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make(map[string]json.RawMessage, len(refs))
	for _, r := range refs {
		def, ok := src[r.Ref]
		if !ok {
			return nil, fmt.Errorf("mcp_servers: ref %q not found in ~/.claude.json (available: %s)", r.Ref, availableKeys(src))
		}
		out[r.Ref] = def
	}
	return out, nil
}

// availableKeys renders the keys of src as a deterministic
// comma-separated list for error messages. Empty maps yield "<none>" so
// the user can distinguish "ref typo" from "user never ran `claude mcp add`".
func availableKeys(src map[string]json.RawMessage) string {
	if len(src) == 0 {
		return "<none>"
	}
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
