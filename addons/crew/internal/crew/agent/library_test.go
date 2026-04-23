package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// writeFile is a tiny helper that writes content under dir/name and fails
// the test on error. Used by LoadTool / ListLibraryTools cases.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestLoadTool(t *testing.T) {
	dir := t.TempDir()

	okYAML := `name: echo
protocol: exec
description: echo back
command: ["/bin/sh","-c","echo hi"]
input_schema:
  text: string
output_schema:
  echoed: string
`

	tests := []struct {
		name    string
		file    string
		content string
		wantErr string // substring; empty = no error
	}{
		{
			name:    "happy path",
			file:    "echo.yaml",
			content: okYAML,
		},
		{
			name: "unknown field rejected",
			file: "echo.yaml",
			content: `name: echo
protocol: exec
command: ["/bin/true"]
foo: bar
`,
			wantErr: "parse",
		},
		{
			name: "bad yaml",
			file: "echo.yaml",
			content: `name: echo
protocol: [oops
`,
			wantErr: "parse",
		},
		{
			name: "filename does not match tool name",
			file: "mismatch.yaml",
			content: `name: echo
protocol: exec
command: ["/bin/true"]
`,
			wantErr: "does not match",
		},
		{
			name: "tool fails validate",
			file: "bad.yaml",
			content: `name: bad
protocol: exec
`,
			wantErr: "validate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sub := t.TempDir()
			_ = dir
			path := writeFile(t, sub, tc.file, tc.content)

			tool, err := LoadTool(path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tool.Name == "" {
					t.Fatalf("expected non-empty tool name")
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestResolveToolRefs(t *testing.T) {
	// Build a library directory with two tools (echo, fetch) so refs can
	// resolve.
	lib := t.TempDir()
	writeFile(t, lib, "echo.yaml", `name: echo
protocol: exec
command: ["/bin/sh","-c","echo hi"]
`)
	writeFile(t, lib, "fetch.yaml", `name: fetch
protocol: http
method: GET
url: https://example.com
`)

	inlineEcho := ToolEntry{
		Name:     "echo",
		Protocol: crew.ToolExec,
		Command:  []string{"/bin/true"},
	}

	t.Run("empty input", func(t *testing.T) {
		got, err := ResolveToolRefs(nil, lib)
		if err != nil {
			t.Fatalf("nil: unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("nil: expected nil result, got %v", got)
		}
	})

	t.Run("all inline", func(t *testing.T) {
		got, err := ResolveToolRefs([]ToolEntry{inlineEcho}, lib)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].Name != "echo" || got[0].Protocol != crew.ToolExec {
			t.Fatalf("unexpected result: %+v", got)
		}
	})

	t.Run("all refs", func(t *testing.T) {
		got, err := ResolveToolRefs([]ToolEntry{{Ref: "echo"}, {Ref: "fetch"}}, lib)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 tools, got %d", len(got))
		}
		gotNames := []string{got[0].Name, got[1].Name}
		wantNames := []string{"echo", "fetch"}
		if !reflect.DeepEqual(gotNames, wantNames) {
			t.Fatalf("order preserved: got %v want %v", gotNames, wantNames)
		}
	})

	t.Run("mixed inline and ref", func(t *testing.T) {
		got, err := ResolveToolRefs([]ToolEntry{
			{Ref: "fetch"},
			inlineEcho,
		}, lib)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 || got[0].Name != "fetch" || got[1].Name != "echo" {
			t.Fatalf("unexpected: %+v", got)
		}
	})

	t.Run("ref with inline fields rejected", func(t *testing.T) {
		_, err := ResolveToolRefs([]ToolEntry{{Ref: "echo", Name: "echo"}}, lib)
		if err == nil || !strings.Contains(err.Error(), "must not set inline fields") {
			t.Fatalf("want inline-fields error, got %v", err)
		}
	})

	t.Run("empty entry rejected", func(t *testing.T) {
		_, err := ResolveToolRefs([]ToolEntry{{}}, lib)
		if err == nil || !strings.Contains(err.Error(), "must declare either ref or name") {
			t.Fatalf("want empty-entry error, got %v", err)
		}
	})

	t.Run("missing ref reports available tools", func(t *testing.T) {
		_, err := ResolveToolRefs([]ToolEntry{{Ref: "ghost"}}, lib)
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, `"ghost"`) {
			t.Fatalf("error should mention missing ref: %s", msg)
		}
		if !strings.Contains(msg, "echo") || !strings.Contains(msg, "fetch") {
			t.Fatalf("error should list available tools: %s", msg)
		}
	})

	t.Run("ref name syntax validated", func(t *testing.T) {
		_, err := ResolveToolRefs([]ToolEntry{{Ref: "BAD NAME"}}, lib)
		if err == nil || !strings.Contains(err.Error(), "must match") {
			t.Fatalf("want ref syntax error, got %v", err)
		}
	})

	t.Run("missing lib dir with inline-only list succeeds", func(t *testing.T) {
		got, err := ResolveToolRefs([]ToolEntry{inlineEcho}, filepath.Join(t.TempDir(), "nope"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("want 1, got %d", len(got))
		}
	})
}

func TestListLibraryTools(t *testing.T) {
	t.Run("missing directory yields empty list", func(t *testing.T) {
		got, err := ListLibraryTools(filepath.Join(t.TempDir(), "nope"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("want empty, got %v", got)
		}
	})

	t.Run("empty directory yields empty list", func(t *testing.T) {
		got, err := ListLibraryTools(t.TempDir())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("want empty, got %v", got)
		}
	})

	t.Run("returns sorted valid names, skips non-yaml and broken", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "zeta.yaml", `name: zeta
protocol: exec
command: ["/bin/true"]
`)
		writeFile(t, dir, "alpha.yaml", `name: alpha
protocol: exec
command: ["/bin/true"]
`)
		writeFile(t, dir, "README.md", "not a tool")
		// broken: filename does not match body
		writeFile(t, dir, "mismatch.yaml", `name: other
protocol: exec
command: ["/bin/true"]
`)
		// broken: invalid yaml
		writeFile(t, dir, "broken.yaml", `: : :`)
		// subdirectory ignored
		if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
			t.Fatalf("mkdir nested: %v", err)
		}

		got, err := ListLibraryTools(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"alpha", "zeta"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
}
