package backend

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Remote MCP servers (type http, sse, ws) cannot sit behind the mcp-filter
// stdio proxy: Claude Code keeps their OAuth tokens in its own credential
// store, keyed to the server entry, so a proxy in between would have no
// credentials to call upstream with. Their tool whitelist is enforced by
// Claude Code itself instead, with the entry passed through verbatim so the
// stored sign-in keeps matching.
//
// Under --permission-mode bypassPermissions allow rules are ignored; only
// deny rules hold, and a deny rule without parentheses removes the matching
// tools from the model's context altogether. So the whitelist is turned into
// the deny rules that cover every OTHER name the server could expose. The
// rules are computed from the whitelist alone, without contacting the
// server, so a tool the server adds later is hidden too.
//
// Two Claude Code behaviours make that complement finite (both checked
// against Claude Code 2.1.x):
//
//   - MCP tools are named mcp__<server>__<tool>, with every character
//     outside [A-Za-z0-9_-] in either part replaced by '_'.
//   - Deny patterns are globs in which '*' matches any run of characters,
//     including none. There are no character classes and no '?'.
//
// Given the whitelist W of a server s, with U the characters used by W:
//
//   - mcp__s__*c* for every character c of the alphabet outside U;
//   - then, walking the trie of W over U, for each node p:
//     mcp__s__p   when p (the root included) is not itself in W,
//     mcp__s__pc* for every c in U that leads to no name of W.
//
// The '.' is kept in the alphabet as insurance should Claude Code ever stop
// rewriting it (MCP allows it in tool names); it costs one rule.
const mcpToolAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-."

const (
	// mcpDenyChunkBytes caps a single --disallowedTools= argv element. Linux
	// refuses any one argument over 128 KiB (MAX_ARG_STRLEN).
	mcpDenyChunkBytes = 64 * 1024
	// mcpDenyMaxBytes caps all deny rules together, well inside ARG_MAX
	// (1 MiB on macOS, typically 2 MiB on Linux, shared with the env). A
	// whitelist of 20 Meta Ads read tools comes to about 100 KiB.
	mcpDenyMaxBytes = 512 * 1024
)

// isStdioEntry reports whether an MCP server entry from ~/.claude.json is a
// stdio server. Claude Code treats an entry without "type" as stdio.
func isStdioEntry(raw json.RawMessage) (bool, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return false, err
	}
	return head.Type == "" || head.Type == "stdio", nil
}

// normalizeMCPName mirrors how Claude Code rewrites server and tool names
// into the mcp__<server>__<tool> form: every character outside
// [A-Za-z0-9_-] becomes '_'.
func normalizeMCPName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// remoteToolDenyRules returns the Claude Code deny rules that leave only the
// whitelisted tools of server visible. See the comment at the top of the
// file for the construction.
func remoteToolDenyRules(server string, allowed []string) []string {
	prefix := "mcp__" + normalizeMCPName(server) + "__"

	names := make(map[string]struct{}, len(allowed))
	used := map[byte]struct{}{}
	for _, a := range allowed {
		n := normalizeMCPName(strings.TrimSpace(a))
		names[n] = struct{}{}
		for i := 0; i < len(n); i++ {
			used[n[i]] = struct{}{}
		}
	}
	var usedChars []byte
	var rules []string
	for i := 0; i < len(mcpToolAlphabet); i++ {
		c := mcpToolAlphabet[i]
		if _, ok := used[c]; ok {
			usedChars = append(usedChars, c)
			continue
		}
		rules = append(rules, prefix+"*"+string(c)+"*")
	}

	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	// walk visits the trie node p; group holds the names that extend p,
	// sorted, so the names under p+c form a contiguous run.
	var walk func(p string, group []string)
	walk = func(p string, group []string) {
		if _, ok := names[p]; !ok {
			rules = append(rules, prefix+p)
		}
		next := map[byte][]string{}
		for _, n := range group {
			if len(n) > len(p) {
				next[n[len(p)]] = append(next[n[len(p)]], n)
			}
		}
		for _, c := range usedChars {
			if sub, ok := next[c]; ok {
				walk(p+string(c), sub)
				continue
			}
			rules = append(rules, prefix+p+string(c)+"*")
		}
	}
	walk("", sorted)
	return rules
}

// disallowedToolsArgs packs deny rules into as few --disallowedTools= argv
// elements as the per-argument limit allows. The "=" form keeps the variadic
// flag from swallowing whatever follows it on the command line; repeated
// occurrences accumulate, and they add to any --disallowedTools the agent's
// own backend.command carries.
func disallowedToolsArgs(rules []string) ([]string, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	total := 0
	for _, r := range rules {
		total += len(r) + 1
	}
	if total > mcpDenyMaxBytes {
		return nil, fmt.Errorf("tool whitelists of remote mcp_servers need %d KiB of deny rules (limit %d KiB): list fewer tools, or use tools: [\"*\"]", total/1024, mcpDenyMaxBytes/1024)
	}
	const flag = "--disallowedTools="
	var args []string
	var cur strings.Builder
	for _, r := range rules {
		if cur.Len() > 0 && cur.Len()+1+len(r) > mcpDenyChunkBytes {
			args = append(args, cur.String())
			cur.Reset()
		}
		if cur.Len() == 0 {
			cur.WriteString(flag)
		} else {
			cur.WriteByte(',')
		}
		cur.WriteString(r)
	}
	return append(args, cur.String()), nil
}
