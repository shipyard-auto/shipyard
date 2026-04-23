package crew

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// setHome pins SHIPYARD_HOME to a tempdir so every subcommand operates on a
// hermetic filesystem and returns the path for convenience.
func setHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("SHIPYARD_HOME", h)
	return h
}

func TestRunToolAdd_ExecHappyPath(t *testing.T) {
	home := setHome(t)
	var buf bytes.Buffer
	err := runToolAdd(&buf, "echo", toolAddFlags{
		protocol:    "exec",
		description: "say hi",
		command:     []string{"/bin/echo", "hello"},
	})
	if err != nil {
		t.Fatalf("runToolAdd: %v", err)
	}
	path := filepath.Join(home, "crew", "tools", "echo.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc toolDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Name != "echo" || doc.Protocol != "exec" || doc.Description != "say hi" {
		t.Fatalf("doc=%+v", doc)
	}
	if len(doc.Command) != 2 || doc.Command[0] != "/bin/echo" || doc.Command[1] != "hello" {
		t.Fatalf("command=%v", doc.Command)
	}
	if !strings.Contains(buf.String(), "echo") {
		t.Fatalf("stdout missing name: %q", buf.String())
	}
	// File must be 0o600 to avoid leaking credentials accidentally pasted
	// in as env/body.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 0600", perm)
	}
}

func TestRunToolAdd_HTTPHappyPath(t *testing.T) {
	home := setHome(t)
	err := runToolAdd(&bytes.Buffer{}, "weather", toolAddFlags{
		protocol: "http",
		method:   "get",
		url:      "https://api.example/weather",
		headers:  []string{"Authorization: Bearer xyz", "X-Trace: 1"},
		body:     `{"q":"sf"}`,
	})
	if err != nil {
		t.Fatalf("runToolAdd: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "crew", "tools", "weather.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc toolDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Protocol != "http" || doc.Method != "GET" || doc.URL != "https://api.example/weather" {
		t.Fatalf("doc=%+v", doc)
	}
	if doc.Headers["Authorization"] != "Bearer xyz" || doc.Headers["X-Trace"] != "1" {
		t.Fatalf("headers=%v", doc.Headers)
	}
	if doc.Body != `{"q":"sf"}` {
		t.Fatalf("body=%q", doc.Body)
	}
}

func TestRunToolAdd_Errors(t *testing.T) {
	cases := []struct {
		name    string
		toolNm  string
		flags   toolAddFlags
		wantSub string
	}{
		{
			name:    "invalid name",
			toolNm:  "Bad-Name",
			flags:   toolAddFlags{protocol: "exec", command: []string{"/x"}},
			wantSub: "invalid name",
		},
		{
			name:    "unknown protocol",
			toolNm:  "t",
			flags:   toolAddFlags{protocol: "grpc"},
			wantSub: "invalid --protocol",
		},
		{
			name:    "exec without command",
			toolNm:  "t",
			flags:   toolAddFlags{protocol: "exec"},
			wantSub: "requires at least one --command",
		},
		{
			name:    "exec with http flags",
			toolNm:  "t",
			flags:   toolAddFlags{protocol: "exec", command: []string{"/x"}, url: "https://x"},
			wantSub: "must not set --method",
		},
		{
			name:    "http bad method",
			toolNm:  "t",
			flags:   toolAddFlags{protocol: "http", method: "TRACE", url: "https://x"},
			wantSub: "requires --method",
		},
		{
			name:    "http without url",
			toolNm:  "t",
			flags:   toolAddFlags{protocol: "http", method: "GET"},
			wantSub: "requires --url",
		},
		{
			name:    "http with command",
			toolNm:  "t",
			flags:   toolAddFlags{protocol: "http", method: "GET", url: "https://x", command: []string{"x"}},
			wantSub: "must not set --command",
		},
		{
			name:    "bad header",
			toolNm:  "t",
			flags:   toolAddFlags{protocol: "http", method: "GET", url: "https://x", headers: []string{"no-colon"}},
			wantSub: "invalid --header",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setHome(t)
			err := runToolAdd(&bytes.Buffer{}, tc.toolNm, tc.flags)
			if err == nil {
				t.Fatalf("want error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err=%v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestRunToolAdd_ExistingRefusesWithoutForce(t *testing.T) {
	home := setHome(t)
	base := toolAddFlags{protocol: "exec", command: []string{"/bin/true"}}
	if err := runToolAdd(&bytes.Buffer{}, "dup", base); err != nil {
		t.Fatalf("first add: %v", err)
	}
	// Same path, no --force: must refuse.
	err := runToolAdd(&bytes.Buffer{}, "dup", base)
	if err == nil {
		t.Fatalf("want error on re-add")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err=%v", err)
	}
	// --force overwrites silently.
	base.force = true
	base.description = "second"
	if err := runToolAdd(&bytes.Buffer{}, "dup", base); err != nil {
		t.Fatalf("force add: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "crew", "tools", "dup.yaml"))
	var doc toolDoc
	_ = yaml.Unmarshal(raw, &doc)
	if doc.Description != "second" {
		t.Fatalf("not overwritten: %+v", doc)
	}
}

func TestRunToolList_EmptyAndPopulated(t *testing.T) {
	setHome(t)

	// Empty library.
	var buf bytes.Buffer
	if err := runToolList(&buf, toolListFlags{}); err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if !strings.Contains(buf.String(), "no tools") {
		t.Fatalf("empty list = %q", buf.String())
	}

	// Add a couple.
	_ = runToolAdd(&bytes.Buffer{}, "alpha", toolAddFlags{protocol: "exec", command: []string{"/x"}, description: "A"})
	_ = runToolAdd(&bytes.Buffer{}, "beta", toolAddFlags{protocol: "http", method: "GET", url: "https://x"})

	buf.Reset()
	if err := runToolList(&buf, toolListFlags{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	out := buf.String()
	// alpha comes before beta (sorted).
	ai := strings.Index(out, "alpha")
	bi := strings.Index(out, "beta")
	if ai < 0 || bi < 0 || ai > bi {
		t.Fatalf("sort broken: %q", out)
	}
	// Header present.
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "PROTOCOL") {
		t.Fatalf("header missing: %q", out)
	}

	// JSON mode.
	buf.Reset()
	if err := runToolList(&buf, toolListFlags{JSON: true}); err != nil {
		t.Fatalf("list json: %v", err)
	}
	var got []toolListEntry
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("parse json: %v (%q)", err, buf.String())
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Fatalf("got=%+v", got)
	}
}

func TestRunToolList_SkipsBrokenFiles(t *testing.T) {
	home := setHome(t)
	libDir := filepath.Join(home, "crew", "tools")
	if err := os.MkdirAll(libDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Valid tool.
	_ = runToolAdd(&bytes.Buffer{}, "good", toolAddFlags{protocol: "exec", command: []string{"/x"}})
	// Invalid yaml file.
	if err := os.WriteFile(filepath.Join(libDir, "broken.yaml"), []byte("not: valid: yaml: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	// Mismatch between filename and name field.
	if err := os.WriteFile(filepath.Join(libDir, "wrong-name.yaml"),
		[]byte("name: other\nprotocol: exec\ncommand: [/x]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Non-yaml file.
	if err := os.WriteFile(filepath.Join(libDir, "README"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runToolList(&buf, toolListFlags{JSON: true}); err != nil {
		t.Fatalf("list: %v", err)
	}
	var got []toolListEntry
	_ = json.Unmarshal(buf.Bytes(), &got)
	if len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("want only \"good\", got %+v", got)
	}
}

func TestRunToolShow_YAMLAndJSON(t *testing.T) {
	setHome(t)
	if err := runToolAdd(&bytes.Buffer{}, "echo", toolAddFlags{
		protocol: "exec", command: []string{"/bin/echo", "hi"}, description: "d",
	}); err != nil {
		t.Fatal(err)
	}

	// YAML form: raw file contents.
	var buf bytes.Buffer
	if err := runToolShow(&buf, "echo", false); err != nil {
		t.Fatalf("show yaml: %v", err)
	}
	if !strings.Contains(buf.String(), "name: echo") || !strings.Contains(buf.String(), "protocol: exec") {
		t.Fatalf("yaml out: %q", buf.String())
	}

	// JSON form.
	buf.Reset()
	if err := runToolShow(&buf, "echo", true); err != nil {
		t.Fatalf("show json: %v", err)
	}
	var doc toolDoc
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("json parse: %v", err)
	}
	if doc.Name != "echo" || doc.Description != "d" {
		t.Fatalf("doc=%+v", doc)
	}
}

func TestRunToolShow_NotFound(t *testing.T) {
	setHome(t)
	err := runToolShow(&bytes.Buffer{}, "ghost", false)
	if err == nil {
		t.Fatalf("want error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunToolShow_InvalidName(t *testing.T) {
	setHome(t)
	err := runToolShow(&bytes.Buffer{}, "Bad-Name", false)
	if err == nil || !strings.Contains(err.Error(), "invalid name") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunToolRm_Happy(t *testing.T) {
	home := setHome(t)
	if err := runToolAdd(&bytes.Buffer{}, "kill", toolAddFlags{protocol: "exec", command: []string{"/x"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "crew", "tools", "kill.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pre-rm stat: %v", err)
	}
	var buf bytes.Buffer
	if err := runToolRm(&buf, "kill", toolRmFlags{}); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should be gone, err=%v", err)
	}
	if !strings.Contains(buf.String(), "removed") {
		t.Fatalf("stdout=%q", buf.String())
	}
}

func TestRunToolRm_NotFound(t *testing.T) {
	setHome(t)
	err := runToolRm(&bytes.Buffer{}, "ghost", toolRmFlags{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunToolRm_BlockedByAgentUser(t *testing.T) {
	home := setHome(t)
	if err := runToolAdd(&bytes.Buffer{}, "shared", toolAddFlags{protocol: "exec", command: []string{"/x"}}); err != nil {
		t.Fatal(err)
	}
	// Create an agent that references the tool by ref.
	agentDir := filepath.Join(home, "crew", "greeter")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentYAML := "schema_version: \"1\"\nname: greeter\ntools:\n  - ref: shared\n"
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte(agentYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// Without --yes: blocked with a useful error mentioning the agent.
	err := runToolRm(&bytes.Buffer{}, "shared", toolRmFlags{})
	if err == nil {
		t.Fatalf("expected block")
	}
	if !strings.Contains(err.Error(), "greeter") || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "crew", "tools", "shared.yaml")); err != nil {
		t.Fatalf("tool should still exist: %v", err)
	}

	// With --yes: removal succeeds.
	if err := runToolRm(&bytes.Buffer{}, "shared", toolRmFlags{yes: true}); err != nil {
		t.Fatalf("force rm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "crew", "tools", "shared.yaml")); !os.IsNotExist(err) {
		t.Fatalf("tool should be gone: %v", err)
	}
}

func TestFindAgentUsers_IgnoresToolsDirAndBrokenFiles(t *testing.T) {
	home := setHome(t)
	crewRoot := filepath.Join(home, "crew")

	// The tools/ sibling directory must not be walked.
	if err := os.MkdirAll(filepath.Join(crewRoot, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Agent that uses the tool.
	good := filepath.Join(crewRoot, "a")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "agent.yaml"),
		[]byte("tools:\n  - ref: target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Agent that uses something else.
	other := filepath.Join(crewRoot, "b")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "agent.yaml"),
		[]byte("tools:\n  - ref: unrelated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Broken yaml must not abort.
	broken := filepath.Join(crewRoot, "c")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "agent.yaml"),
		[]byte("not: valid: ["), 0o600); err != nil {
		t.Fatal(err)
	}

	users, err := findAgentUsers(crewRoot, "target")
	if err != nil {
		t.Fatalf("findAgentUsers: %v", err)
	}
	if len(users) != 1 || users[0] != "a" {
		t.Fatalf("users=%v", users)
	}
}

func TestFindAgentUsers_NoCrewDir(t *testing.T) {
	// crewRoot absent → no error, empty slice.
	users, err := findAgentUsers(filepath.Join(t.TempDir(), "absent"), "x")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(users) != 0 {
		t.Fatalf("users=%v", users)
	}
}

func TestNewToolCmd_Wiring(t *testing.T) {
	// The parent command should expose the four CRUD subcommands.
	cmd := newToolCmd()
	got := map[string]bool{}
	for _, c := range cmd.Commands() {
		got[c.Name()] = true
	}
	for _, want := range []string{"add", "list", "show", "rm"} {
		if !got[want] {
			t.Errorf("missing subcommand %q (got %v)", want, got)
		}
	}
}
