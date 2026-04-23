package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeMCPServe(code int, err error) func(context.Context, mcpServeRequest) (int, error) {
	return func(_ context.Context, _ mcpServeRequest) (int, error) {
		return code, err
	}
}

func TestMCPServeFlagParsing(t *testing.T) {
	longName := strings.Repeat("a", 64)

	cases := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{name: "agent missing", args: []string{"mcp-serve"}, wantExit: ExitInvalidInput, wantStderr: "invalid --agent"},
		{name: "agent uppercase", args: []string{"mcp-serve", "--agent", "Bad"}, wantExit: ExitInvalidInput, wantStderr: "invalid --agent"},
		{name: "agent too long", args: []string{"mcp-serve", "--agent", longName}, wantExit: ExitInvalidInput, wantStderr: "invalid --agent"},
		{name: "unknown flag", args: []string{"mcp-serve", "--wat"}, wantExit: ExitInvalidInput, wantStderr: "flag provided but not defined"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, stderr := newDeps(tc.args, nil)
			deps.RunMCPServe = fakeMCPServe(ExitOK, nil)
			got := run(context.Background(), deps)
			if got != tc.wantExit {
				t.Fatalf("exit = %d, want %d (stderr=%q)", got, tc.wantExit, stderr.String())
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr missing %q; got %q", tc.wantStderr, stderr.String())
			}
		})
	}
}

func TestMCPServePathResolution(t *testing.T) {
	var captured mcpServeRequest
	deps, _, _ := newDeps([]string{"mcp-serve", "--agent", "alpha"}, map[string]string{"SHIPYARD_HOME": "/tmp/sy-home"})
	deps.RunMCPServe = func(_ context.Context, req mcpServeRequest) (int, error) {
		captured = req
		return ExitOK, nil
	}
	if got := run(context.Background(), deps); got != ExitOK {
		t.Fatalf("exit=%d", got)
	}
	if captured.AgentName != "alpha" {
		t.Fatalf("AgentName=%q", captured.AgentName)
	}
	wantDir := filepath.Join("/tmp/sy-home", "crew", "alpha")
	if captured.AgentDir != wantDir {
		t.Fatalf("AgentDir=%q want %q", captured.AgentDir, wantDir)
	}
}

func TestMCPServeHandlerError(t *testing.T) {
	deps, _, stderr := newDeps([]string{"mcp-serve", "--agent", "alpha"}, nil)
	deps.RunMCPServe = fakeMCPServe(ExitOnDemandInternal, errors.New("boom"))
	got := run(context.Background(), deps)
	if got != ExitOnDemandInternal {
		t.Fatalf("exit=%d", got)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Fatalf("stderr missing error: %q", stderr.String())
	}
}

// TestMCPServeEndToEnd_Echo exercises the full subcommand: set up a
// temporary agent on disk with one `exec` tool that echoes JSON, spawn
// defaultRunMCPServe against a stdin containing initialize + tools/list +
// tools/call + EOF, and verify the responses. This mirrors what Claude
// Code will do when --mcp-config points at us.
func TestMCPServeEndToEnd_Echo(t *testing.T) {
	home := t.TempDir()
	agentName := "greeter"
	agentDir := filepath.Join(home, "crew", agentName)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Tool command: a tiny shell that emits a fixed envelope. Using
	// /bin/sh keeps the test portable across macOS/Linux. The command is
	// written as a YAML block sequence so we avoid the quoting hell of
	// flow-style arrays containing JSON.
	agentYAML := fmt.Sprintf(`schema_version: "1"
name: %s
description: test agent
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
  - name: echo
    protocol: exec
    command:
      - /bin/sh
      - -c
      - 'printf %%s ''{"ok":true,"data":{"echoed":"fixed"}}'''
    description: always echo fixed payload
    input_schema:
      text: string
    output_schema:
      echoed: string
`, agentName)
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte(agentYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "prompt.md"), []byte("p"), 0o600); err != nil {
		t.Fatal(err)
	}

	frames := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
	}, "\n") + "\n"

	req := mcpServeRequest{
		AgentName: agentName,
		AgentDir:  agentDir,
		Stdin:     strings.NewReader(frames),
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
	}
	code, err := defaultRunMCPServe(context.Background(), req)
	if err != nil {
		t.Fatalf("serve err: %v", err)
	}
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}

	// Parse responses and validate each in turn.
	out := req.Stdout.(*bytes.Buffer).String()
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response %q: %v", line, err)
		}
		responses = append(responses, m)
	}
	if len(responses) != 3 {
		t.Fatalf("want 3 responses (init, list, call), got %d: %+v", len(responses), responses)
	}

	// initialize
	if id, _ := responses[0]["id"].(float64); id != 1 {
		t.Fatalf("init id: %v", responses[0]["id"])
	}
	// tools/list
	listResult := responses[1]["result"].(map[string]any)
	toolsArr := listResult["tools"].([]any)
	if len(toolsArr) != 1 || toolsArr[0].(map[string]any)["name"] != "echo" {
		t.Fatalf("tools/list: %+v", toolsArr)
	}
	// tools/call
	callResult := responses[2]["result"].(map[string]any)
	if isErr, _ := callResult["isError"].(bool); isErr {
		t.Fatalf("call reported error: %+v", callResult)
	}
	sc, ok := callResult["structuredContent"].(map[string]any)
	if !ok || sc["echoed"] != "fixed" {
		t.Fatalf("structuredContent: %+v", callResult["structuredContent"])
	}
}

// TestMCPServeLoadError fails cleanly when the agent directory is missing.
func TestMCPServeLoadError(t *testing.T) {
	req := mcpServeRequest{
		AgentName: "ghost",
		AgentDir:  filepath.Join(t.TempDir(), "missing"),
		Stdin:     strings.NewReader(""),
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
	}
	code, err := defaultRunMCPServe(context.Background(), req)
	if code != ExitInvalidConfig {
		t.Fatalf("exit=%d, want %d", code, ExitInvalidConfig)
	}
	if err == nil {
		t.Fatalf("expected error")
	}
}
