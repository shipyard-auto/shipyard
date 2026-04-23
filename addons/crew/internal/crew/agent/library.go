package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// ToolEntry is the intermediate form decoded from the agent.yaml tools list.
// Each entry is either inline (Ref == "") with protocol/command/etc. fields
// populated, or a reference (Ref != "") to a reusable tool defined in
// ~/.shipyard/crew/tools/<ref>.yaml. A single entry may not mix the two
// forms — ResolveToolRefs rejects that.
type ToolEntry struct {
	Ref          string            `yaml:"ref,omitempty"`
	Name         string            `yaml:"name,omitempty"`
	Protocol     crew.ToolProtocol `yaml:"protocol,omitempty"`
	Description  string            `yaml:"description,omitempty"`
	InputSchema  map[string]string `yaml:"input_schema,omitempty"`
	OutputSchema map[string]string `yaml:"output_schema,omitempty"`
	Command      []string          `yaml:"command,omitempty"`
	Method       string            `yaml:"method,omitempty"`
	URL          string            `yaml:"url,omitempty"`
	Headers      map[string]string `yaml:"headers,omitempty"`
	Body         string            `yaml:"body,omitempty"`
}

// IsRef reports whether this entry is a reference to the shared library.
// An entry with Ref set may not also carry any inline field.
func (e ToolEntry) IsRef() bool { return strings.TrimSpace(e.Ref) != "" }

// toInlineTool converts the inline form of a ToolEntry into a crew.Tool.
// Callers must ensure e.IsRef() is false before calling.
func (e ToolEntry) toInlineTool() crew.Tool {
	return crew.Tool{
		Name:         e.Name,
		Protocol:     e.Protocol,
		Description:  e.Description,
		InputSchema:  e.InputSchema,
		OutputSchema: e.OutputSchema,
		Command:      e.Command,
		Method:       e.Method,
		URL:          e.URL,
		Headers:      e.Headers,
		Body:         e.Body,
	}
}

// LoadTool reads a single tool definition from a YAML file (one tool per
// file). It enforces KnownFields(true) so typos are rejected, then calls
// Tool.Validate so every tool stored in the library is syntactically
// valid. The filename (without .yaml) must match the tool's name — this
// keeps `shipyard crew tool list` trivially deterministic and mirrors the
// agent directory convention.
func LoadTool(path string) (crew.Tool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return crew.Tool{}, fmt.Errorf("load tool %s: %w", path, err)
	}

	var t crew.Tool
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return crew.Tool{}, fmt.Errorf("load tool %s: parse: %w", path, err)
	}

	// File naming contract: <name>.yaml == t.Name.
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if base != t.Name {
		return crew.Tool{}, fmt.Errorf("load tool %s: filename %q does not match tool name %q", path, base, t.Name)
	}

	if err := t.Validate(); err != nil {
		return crew.Tool{}, fmt.Errorf("load tool %s: validate: %w", path, err)
	}
	return t, nil
}

// ResolveToolRefs produces the final tool list for an agent by merging
// inline entries with references resolved against libDir. libDir is the
// absolute path to ~/.shipyard/crew/tools; if the directory does not exist
// and no entry is a ref, resolution still succeeds with an empty library.
//
// Contract:
//   - An entry with Ref set and any inline field populated is rejected.
//   - An entry with neither Ref nor Name is rejected (empty entry).
//   - A ref that does not resolve to a file in libDir is rejected with
//     the list of available library tools in the error message.
//   - Duplicate tool names across the merged list are rejected at the agent
//     level (Agent.Validate), not here — ResolveToolRefs preserves order
//     and emits duplicates so the higher layer sees them.
func ResolveToolRefs(entries []ToolEntry, libDir string) ([]crew.Tool, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	out := make([]crew.Tool, 0, len(entries))
	for i, e := range entries {
		if e.IsRef() {
			if hasInlineFields(e) {
				return nil, fmt.Errorf("tools[%d]: entry with ref %q must not set inline fields", i, e.Ref)
			}
			t, err := resolveRef(e.Ref, libDir)
			if err != nil {
				return nil, fmt.Errorf("tools[%d]: %w", i, err)
			}
			out = append(out, t)
			continue
		}
		if strings.TrimSpace(e.Name) == "" {
			return nil, fmt.Errorf("tools[%d]: entry must declare either ref or name", i)
		}
		out = append(out, e.toInlineTool())
	}
	return out, nil
}

func hasInlineFields(e ToolEntry) bool {
	return e.Name != "" ||
		e.Protocol != "" ||
		e.Description != "" ||
		len(e.InputSchema) > 0 ||
		len(e.OutputSchema) > 0 ||
		len(e.Command) > 0 ||
		e.Method != "" ||
		e.URL != "" ||
		len(e.Headers) > 0 ||
		e.Body != ""
}

func resolveRef(ref, libDir string) (crew.Tool, error) {
	if !crew.ToolNameRe.MatchString(ref) {
		return crew.Tool{}, fmt.Errorf("ref %q: must match %s", ref, crew.ToolNameRe)
	}
	path := filepath.Join(libDir, ref+".yaml")
	t, err := LoadTool(path)
	if err == nil {
		return t, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		available, _ := ListLibraryTools(libDir)
		return crew.Tool{}, fmt.Errorf("ref %q: tool not found in %s (available: %s)", ref, libDir, strings.Join(available, ","))
	}
	return crew.Tool{}, err
}

// ListLibraryTools returns the sorted list of tool names available in
// libDir. A missing directory is not an error — it yields an empty list.
// Non-yaml files and files whose body fails LoadTool are silently skipped
// so a single broken tool cannot paralyse the CLI listing; callers that
// need to surface broken tools can iterate with LoadTool directly.
func ListLibraryTools(libDir string) ([]string, error) {
	entries, err := os.ReadDir(libDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		base := strings.TrimSuffix(name, ".yaml")
		if _, err := LoadTool(filepath.Join(libDir, name)); err != nil {
			continue
		}
		names = append(names, base)
	}
	sort.Strings(names)
	return names, nil
}
