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
// projects.<path>.mcpServers) are deliberately NOT read in v1 — see
// docs/crew/roadmap.md for the follow-up.
const claudeConfigFile = ".claude.json"

// LoadClaudeMCPs reads the root-level mcpServers map from ~/.claude.json.
// Each entry's raw JSON (the full {type, command, args, env, ...} value as
// written by Claude Code) is preserved byte-for-byte so we can pass it
// through to --mcp-config without interpreting fields the spec may extend
// later.
//
// A missing file is not an error — it returns (nil, nil), matching the
// common case of a user who has never run `claude mcp add`. A present but
// malformed file IS an error: we prefer to fail loud rather than silently
// lose servers the user believes are configured.
//
// userHome is injected so tests can point at a tempdir. nil means use
// os.UserHomeDir.
func LoadClaudeMCPs(userHome func() (string, error)) (map[string]json.RawMessage, error) {
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

	// Only decode the field we need; everything else in ~/.claude.json is
	// out of our concern.
	var shell struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &shell); err != nil {
		return nil, fmt.Errorf("claude config: parse %s: %w", path, err)
	}
	return shell.MCPServers, nil
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
