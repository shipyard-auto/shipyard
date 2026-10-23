package backend

import (
	"math/rand"
	"strings"
	"testing"
)

// claudeGlobMatch mirrors how Claude Code matches a deny rule without
// parentheses against a tool name: the whole name must match, and '*'
// matches any run of characters, including none.
func claudeGlobMatch(pattern, name string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == name
	}
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	rest := name[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, mid)
		if i < 0 {
			return false
		}
		rest = rest[i+len(mid):]
	}
	return len(rest) >= len(last) && strings.HasSuffix(rest, last)
}

// visible reports whether a tool named name (already in mcp__s__t form)
// survives the given deny rules.
func visible(rules []string, name string) bool {
	for _, r := range rules {
		if claudeGlobMatch(r, name) {
			return false
		}
	}
	return true
}

func TestClaudeGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"mcp__s__a", "mcp__s__a", true},
		{"mcp__s__a", "mcp__s__ab", false},
		{"mcp__s__a*", "mcp__s__a", true},
		{"mcp__s__a*", "mcp__s__abc", true},
		{"mcp__s__*x*", "mcp__s__x", true},
		{"mcp__s__*x*", "mcp__s__axb", true},
		{"mcp__s__*x*", "mcp__s__ab", false},
		{"mcp__s__*_x", "mcp__s__get_ads_x", true},
		{"mcp__s__*_x", "mcp__s__get_ads_xy", false},
	}
	for _, c := range cases {
		if got := claudeGlobMatch(c.pattern, c.name); got != c.want {
			t.Errorf("match(%q, %q)=%v want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestRemoteToolDenyRules_LeavesOnlyWhitelist(t *testing.T) {
	allowed := []string{"get_ads", "get_ad_accounts", "get_adsets", "get_insights", "Read_A"}
	rules := remoteToolDenyRules("meta-ads", allowed)

	for _, a := range allowed {
		if !visible(rules, "mcp__meta-ads__"+a) {
			t.Errorf("whitelisted %q is hidden", a)
		}
	}
	// Prefixes, extensions, case variants, neighbours, characters no
	// whitelisted name uses and names Claude Code would never produce.
	decoys := []string{
		"", "g", "ge", "get", "get_", "get_ad", "get_ads_x", "get_adsX", "get_ads-", "get_ads.y",
		"Get_ads", "GET_ADS", "get_adset", "get_adsets_v2", "read_a", "Read_a", "Read_AB",
		"create_campaign", "update_ad", "delete_adset", "pause_ad", "z", "9", "_", "-",
		"get_insights2", "get_ad_account", "get_ad_accountss",
	}
	for _, d := range decoys {
		if visible(rules, "mcp__meta-ads__"+d) {
			t.Errorf("decoy %q stays visible", d)
		}
	}
}

func TestRemoteToolDenyRules_RandomNamesVisibleOnlyWhenWhitelisted(t *testing.T) {
	allowed := []string{"list_campaigns", "list_ads", "get_ad", "insights"}
	rules := remoteToolDenyRules("srv", allowed)
	set := map[string]bool{}
	for _, a := range allowed {
		set[a] = true
	}

	rng := rand.New(rand.NewSource(1))
	// Bias the alphabet toward the characters of the whitelist so the
	// generated names walk deep into the trie, not just off its root.
	alphabet := mcpToolAlphabet + "listcampagnsdeiht_____"
	mutate := func(s string) string {
		b := []byte(s)
		switch rng.Intn(3) {
		case 0: // flip one character
			if len(b) > 0 {
				b[rng.Intn(len(b))] = alphabet[rng.Intn(len(alphabet))]
			}
		case 1: // truncate
			if len(b) > 0 {
				b = b[:rng.Intn(len(b))]
			}
		default: // extend
			b = append(b, alphabet[rng.Intn(len(alphabet))])
		}
		return string(b)
	}
	for i := 0; i < 20000; i++ {
		var name string
		if i%2 == 0 {
			name = mutate(allowed[rng.Intn(len(allowed))])
		} else {
			n := 1 + rng.Intn(16)
			b := make([]byte, n)
			for j := range b {
				b[j] = alphabet[rng.Intn(len(alphabet))]
			}
			name = string(b)
		}
		if got := visible(rules, "mcp__srv__"+name); got != set[name] {
			t.Fatalf("name %q: visible=%v, whitelisted=%v", name, got, set[name])
		}
	}
}

func TestRemoteToolDenyRules_NormalizesNames(t *testing.T) {
	// Claude Code exposes server "meta.ads" and tool "get.ads" as
	// mcp__meta_ads__get_ads; the rules must be written against that form.
	rules := remoteToolDenyRules("meta.ads", []string{"get.ads"})
	if !visible(rules, "mcp__meta_ads__get_ads") {
		t.Fatalf("normalized whitelisted tool is hidden")
	}
	if visible(rules, "mcp__meta_ads__get_adz") {
		t.Fatalf("neighbour stays visible")
	}
	for _, r := range rules {
		if !strings.HasPrefix(r, "mcp__meta_ads__") {
			t.Fatalf("rule %q is not scoped to the normalized server name", r)
		}
	}
}

func TestRemoteToolDenyRules_ScopedToServer(t *testing.T) {
	rules := remoteToolDenyRules("meta-ads", []string{"get_ads"})
	for _, other := range []string{"mcp__shipyard-crew-internal__echo", "mcp__chrome__click", "Bash", "mcp__meta-ads2__x"} {
		if !visible(rules, other) {
			t.Errorf("rules leak onto %q", other)
		}
	}
}

func TestNormalizeMCPName(t *testing.T) {
	cases := map[string]string{
		"get_ads":   "get_ads",
		"a-b":       "a-b",
		"do.thing":  "do_thing",
		"x y":       "x_y",
		"ação":      "a__o",
		"Read_A":    "Read_A",
		"meta.ads2": "meta_ads2",
	}
	for in, want := range cases {
		if got := normalizeMCPName(in); got != want {
			t.Errorf("normalize(%q)=%q want %q", in, got, want)
		}
	}
}

func TestIsStdioEntry(t *testing.T) {
	cases := map[string]bool{
		`{"command":"npx"}`:                        true,
		`{"type":"stdio","command":"npx"}`:         true,
		`{"type":"http","url":"https://x/mcp"}`:    false,
		`{"type":"sse","url":"https://x/sse"}`:     false,
		`{"type":"ws","url":"wss://x/mcp"}`:        false,
		`{"type":"http","url":"u","headers":{}}`:   false,
		`{"type":"stdio","command":"x","env":{}}`:  true,
		`{"command":"x","args":["--type","http"]}`: true,
	}
	for raw, want := range cases {
		got, err := isStdioEntry([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got != want {
			t.Errorf("isStdioEntry(%s)=%v want %v", raw, got, want)
		}
	}
	if _, err := isStdioEntry([]byte(`[1]`)); err == nil {
		t.Fatalf("want error for non-object entry")
	}
}

func TestDisallowedToolsArgs_ChunksUnderArgLimit(t *testing.T) {
	var rules []string
	for i := 0; i < 6000; i++ {
		rules = append(rules, "mcp__srv__"+strings.Repeat("x", 20)+string(rune('a'+i%26)))
	}
	args, err := disallowedToolsArgs(rules)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(args) < 2 {
		t.Fatalf("want several chunks for %d rules, got %d", len(rules), len(args))
	}
	var back []string
	for _, a := range args {
		if !strings.HasPrefix(a, "--disallowedTools=") {
			t.Fatalf("element without = form: %.40q", a)
		}
		if len(a) > mcpDenyChunkBytes+len("--disallowedTools=") {
			t.Fatalf("element of %d bytes over the chunk limit", len(a))
		}
		back = append(back, strings.Split(strings.TrimPrefix(a, "--disallowedTools="), ",")...)
	}
	if len(back) != len(rules) {
		t.Fatalf("rules lost in chunking: %d of %d", len(back), len(rules))
	}
}

func TestDisallowedToolsArgs_Empty(t *testing.T) {
	args, err := disallowedToolsArgs(nil)
	if err != nil || args != nil {
		t.Fatalf("want nil, nil; got %v, %v", args, err)
	}
}

func TestDisallowedToolsArgs_RejectsOversizedWhitelist(t *testing.T) {
	var rules []string
	for i := 0; i < mcpDenyMaxBytes/10; i++ {
		rules = append(rules, "mcp__srv__x")
	}
	if _, err := disallowedToolsArgs(rules); err == nil || !strings.Contains(err.Error(), `tools: ["*"]`) {
		t.Fatalf("want size error pointing at the wildcard, got %v", err)
	}
}
