// Package agent provides loading and writing of agent.yaml files from disk.
package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// yamlDoc is the on-disk shape used by Write — Tools here are always the
// resolved, inline form (refs are expanded when the Agent is read, and the
// Agent value carries only crew.Tool). Keeping write and load shapes in
// separate types isolates each from the other's quirks.
type yamlDoc struct {
	SchemaVersion string              `yaml:"schema_version"`
	Name          string              `yaml:"name"`
	Description   string              `yaml:"description"`
	Backend       crew.Backend        `yaml:"backend"`
	Execution     crew.Execution      `yaml:"execution"`
	Conversation  crew.Conversation   `yaml:"conversation"`
	Triggers      []crew.Trigger      `yaml:"triggers"`
	Tools         []crew.Tool         `yaml:"tools"`
	MCPServers    []crew.MCPServerRef `yaml:"mcp_servers,omitempty"`
}

// loadDoc is the on-disk shape used by Load. Tools are decoded into the
// intermediate ToolEntry form so each entry can be either inline (matching
// crew.Tool) or a reference ({ref: <name>}) to ~/.shipyard/crew/tools/<name>.yaml.
type loadDoc struct {
	SchemaVersion string              `yaml:"schema_version"`
	Name          string              `yaml:"name"`
	Description   string              `yaml:"description"`
	Backend       crew.Backend        `yaml:"backend"`
	Execution     crew.Execution      `yaml:"execution"`
	Conversation  crew.Conversation   `yaml:"conversation"`
	Triggers      []crew.Trigger      `yaml:"triggers"`
	Tools         []ToolEntry         `yaml:"tools"`
	MCPServers    []crew.MCPServerRef `yaml:"mcp_servers"`
}

// Load reads an agent directory and returns a fully-resolved *crew.Agent.
// Tool references in agent.yaml (entries of the form `- ref: <name>`) are
// resolved against the sibling library directory next to `dir`: for
// dir == ~/.shipyard/crew/<agent>, the library is ~/.shipyard/crew/tools.
// A missing library directory is acceptable as long as no entry is a ref.
func Load(dir string) (*crew.Agent, error) {
	yamlPath := filepath.Join(dir, "agent.yaml")
	promptPath := filepath.Join(dir, "prompt.md")

	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("load agent %s: read agent.yaml: %w", dir, err)
	}

	var doc loadDoc
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("load agent %s: parse agent.yaml: %w", dir, err)
	}

	if doc.SchemaVersion != crew.SchemaVersion {
		return nil, fmt.Errorf("load agent %s: unsupported schema_version %q (expected %q)", dir, doc.SchemaVersion, crew.SchemaVersion)
	}

	if _, err := os.Stat(promptPath); err != nil {
		return nil, fmt.Errorf("load agent %s: prompt.md: %w", dir, err)
	}

	libDir := filepath.Join(filepath.Dir(dir), "tools")
	resolvedTools, err := ResolveToolRefs(doc.Tools, libDir)
	if err != nil {
		return nil, fmt.Errorf("load agent %s: resolve tools: %w", dir, err)
	}

	a := &crew.Agent{
		Name:         doc.Name,
		Description:  doc.Description,
		Backend:      doc.Backend,
		Execution:    doc.Execution,
		Conversation: doc.Conversation,
		Triggers:     doc.Triggers,
		Tools:        resolvedTools,
		MCPServers:   doc.MCPServers,
		Dir:          dir,
		PromptPath:   promptPath,
	}
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("load agent %s: validate: %w", dir, err)
	}
	return a, nil
}
