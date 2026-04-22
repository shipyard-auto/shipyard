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

type yamlDoc struct {
	SchemaVersion string            `yaml:"schema_version"`
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	Backend       crew.Backend      `yaml:"backend"`
	Execution     crew.Execution    `yaml:"execution"`
	Conversation  crew.Conversation `yaml:"conversation"`
	Triggers      []crew.Trigger    `yaml:"triggers"`
	Tools         []crew.Tool       `yaml:"tools"`
}

func Load(dir string) (*crew.Agent, error) {
	yamlPath := filepath.Join(dir, "agent.yaml")
	promptPath := filepath.Join(dir, "prompt.md")

	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("load agent %s: read agent.yaml: %w", dir, err)
	}

	var doc yamlDoc
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

	a := &crew.Agent{
		Name:         doc.Name,
		Description:  doc.Description,
		Backend:      doc.Backend,
		Execution:    doc.Execution,
		Conversation: doc.Conversation,
		Triggers:     doc.Triggers,
		Tools:        doc.Tools,
		Dir:          dir,
		PromptPath:   promptPath,
	}
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("load agent %s: validate: %w", dir, err)
	}
	return a, nil
}
