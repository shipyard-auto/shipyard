package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_Good(t *testing.T) {
	a, err := Load("testdata/good")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Name != "promo-hunter" {
		t.Errorf("name=%q", a.Name)
	}
	if a.Dir != "testdata/good" {
		t.Errorf("dir=%q", a.Dir)
	}
	if a.PromptPath != filepath.Join("testdata/good", "prompt.md") {
		t.Errorf("promptPath=%q", a.PromptPath)
	}
	if len(a.Tools) != 2 {
		t.Errorf("tools=%d", len(a.Tools))
	}
	if len(a.Triggers) != 1 || a.Triggers[0].Type != "cron" {
		t.Errorf("triggers=%+v", a.Triggers)
	}
}

func TestLoad_MissingPrompt(t *testing.T) {
	_, err := Load("testdata/missing-prompt")
	if err == nil || !strings.Contains(err.Error(), "prompt.md") {
		t.Fatalf("want prompt.md error, got %v", err)
	}
}

func TestLoad_InvalidValidation(t *testing.T) {
	_, err := Load("testdata/invalid-validation")
	if err == nil || !strings.Contains(err.Error(), "validate") {
		t.Fatalf("want validate error, got %v", err)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	_, err := Load("testdata/malformed-yaml")
	if err == nil || !strings.Contains(err.Error(), "parse agent.yaml") {
		t.Fatalf("want parse error, got %v", err)
	}
}

func TestLoad_BadSchemaVersion(t *testing.T) {
	_, err := Load("testdata/bad-schema-version")
	if err == nil || !strings.Contains(err.Error(), "unsupported schema_version") {
		t.Fatalf("want schema version error, got %v", err)
	}
}

func TestLoad_NonExistentDir(t *testing.T) {
	_, err := Load("testdata/does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "read agent.yaml") {
		t.Fatalf("want read error, got %v", err)
	}
}

// buildAgentTree sets up a ~/.shipyard/crew/<agent> layout rooted at
// t.TempDir(), returning (root, agentDir). Caller writes agent.yaml and
// prompt.md under agentDir, and optionally tool library files under
// root/tools/.
func buildAgentTree(t *testing.T, agentName string) (string, string) {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, agentName)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent: %v", err)
	}
	return root, agentDir
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoad_ResolvesToolRef(t *testing.T) {
	root, agentDir := buildAgentTree(t, "greeter")
	writeTestFile(t, filepath.Join(root, "tools", "echo.yaml"), `name: echo
protocol: exec
command: ["/bin/true"]
description: echo tool
`)
	writeTestFile(t, filepath.Join(agentDir, "agent.yaml"), `schema_version: "1"
name: greeter
description: ""
backend:
  type: cli
  command: ["claude","--print"]
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateless
triggers: []
tools:
  - ref: echo
`)
	writeTestFile(t, filepath.Join(agentDir, "prompt.md"), "p")

	a, err := Load(agentDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(a.Tools) != 1 || a.Tools[0].Name != "echo" || a.Tools[0].Description != "echo tool" {
		t.Fatalf("ref not resolved: %+v", a.Tools)
	}
}

func TestLoad_MissingRefFails(t *testing.T) {
	_, agentDir := buildAgentTree(t, "greeter")
	writeTestFile(t, filepath.Join(agentDir, "agent.yaml"), `schema_version: "1"
name: greeter
description: ""
backend:
  type: cli
  command: ["claude","--print"]
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateless
triggers: []
tools:
  - ref: ghost
`)
	writeTestFile(t, filepath.Join(agentDir, "prompt.md"), "p")

	_, err := Load(agentDir)
	if err == nil || !strings.Contains(err.Error(), "resolve tools") {
		t.Fatalf("want resolve tools error, got %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error should name missing ref: %v", err)
	}
}

func TestLoad_MixedInlineAndRef(t *testing.T) {
	root, agentDir := buildAgentTree(t, "greeter")
	writeTestFile(t, filepath.Join(root, "tools", "echo.yaml"), `name: echo
protocol: exec
command: ["/bin/true"]
`)
	writeTestFile(t, filepath.Join(agentDir, "agent.yaml"), `schema_version: "1"
name: greeter
description: ""
backend:
  type: cli
  command: ["claude","--print"]
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateless
triggers: []
tools:
  - ref: echo
  - name: inline_tool
    protocol: exec
    command: ["/bin/true"]
`)
	writeTestFile(t, filepath.Join(agentDir, "prompt.md"), "p")

	a, err := Load(agentDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(a.Tools) != 2 || a.Tools[0].Name != "echo" || a.Tools[1].Name != "inline_tool" {
		t.Fatalf("unexpected tools: %+v", a.Tools)
	}
}

func TestLoad_MCPServers(t *testing.T) {
	_, agentDir := buildAgentTree(t, "greeter")
	writeTestFile(t, filepath.Join(agentDir, "agent.yaml"), `schema_version: "1"
name: greeter
description: ""
backend:
  type: cli
  command: ["claude","--print"]
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateless
triggers: []
tools: []
mcp_servers:
  - ref: chrome-devtools
  - ref: playwright
`)
	writeTestFile(t, filepath.Join(agentDir, "prompt.md"), "p")

	a, err := Load(agentDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(a.MCPServers) != 2 {
		t.Fatalf("want 2 mcp_servers, got %d", len(a.MCPServers))
	}
	if a.MCPServers[0].Ref != "chrome-devtools" || a.MCPServers[1].Ref != "playwright" {
		t.Fatalf("unexpected mcp_servers: %+v", a.MCPServers)
	}
}

func TestLoad_OutputSchemaRoundtrip(t *testing.T) {
	_, agentDir := buildAgentTree(t, "greeter")
	writeTestFile(t, filepath.Join(agentDir, "agent.yaml"), `schema_version: "1"
name: greeter
description: ""
backend:
  type: cli
  command: ["claude","--print"]
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateless
triggers: []
tools:
  - name: tool_a
    protocol: exec
    command: ["/bin/true"]
    input_schema:
      x: string
    output_schema:
      y: number
`)
	writeTestFile(t, filepath.Join(agentDir, "prompt.md"), "p")

	a, err := Load(agentDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := a.Tools[0].OutputSchema["y"]; got != "number" {
		t.Fatalf("output_schema not preserved: %v", a.Tools[0].OutputSchema)
	}
}

func TestLoad_UnknownField(t *testing.T) {
	dir := t.TempDir()
	yaml := `schema_version: "1"
name: u
description: ""
backend:
  type: cli
  command: ["x"]
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateless
triggers: []
tools: []
foo: bar
`
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("p"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "parse agent.yaml") {
		t.Fatalf("want parse error from unknown field, got %v", err)
	}
}
