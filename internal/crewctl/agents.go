package crewctl

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// AgentInfo is a minimal, read-only description of a crew agent suitable for
// cross-package consumers (e.g. the fairway wizard) that need to reason about
// which agents exist without linking against addons/crew internals.
type AgentInfo struct {
	Name        string
	Description string
	Backend     string
	Mode        string   // execution mode: on-demand, service, cron
	Pool        string   // execution pool name (optional)
	Triggers    []string // distinct trigger types declared in agent.yaml
}

// agentDoc mirrors the tolerant subset of fields we read from agent.yaml.
// It duplicates the internal/cli/crew struct on purpose: core packages must
// not import addons/crew/internal/*.
type agentDoc struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Backend     struct {
		Type string `yaml:"type"`
	} `yaml:"backend"`
	Execution struct {
		Mode string `yaml:"mode"`
		Pool string `yaml:"pool"`
	} `yaml:"execution"`
	Triggers []struct {
		Type string `yaml:"type"`
	} `yaml:"triggers"`
}

// ListAgents scans ~/.shipyard/crew/<name>/agent.yaml and returns one entry
// per directory that parses cleanly. Directories without agent.yaml or with a
// malformed YAML are skipped silently — callers that need diagnostics should
// use the list command's verbose mode directly.
//
// home is the shipyard state root (usually ~/.shipyard). When home is empty
// the user home dir is resolved via os.UserHomeDir.
func ListAgents(home string) ([]AgentInfo, error) {
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("crewctl: resolve home: %w", err)
		}
		home = filepath.Join(h, ".shipyard")
	}

	crewDir := filepath.Join(home, "crew")
	entries, err := os.ReadDir(crewDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("crewctl: read %s: %w", crewDir, err)
	}

	out := make([]AgentInfo, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(crewDir, e.Name(), "agent.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var doc agentDoc
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			continue
		}
		name := doc.Name
		if name == "" {
			name = e.Name()
		}
		out = append(out, AgentInfo{
			Name:        name,
			Description: doc.Description,
			Backend:     doc.Backend.Type,
			Mode:        doc.Execution.Mode,
			Pool:        doc.Execution.Pool,
			Triggers:    distinctTriggers(doc),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func distinctTriggers(doc agentDoc) []string {
	seen := map[string]struct{}{}
	triggers := make([]string, 0, len(doc.Triggers))
	for _, t := range doc.Triggers {
		if t.Type == "" {
			continue
		}
		if _, dup := seen[t.Type]; dup {
			continue
		}
		seen[t.Type] = struct{}{}
		triggers = append(triggers, t.Type)
	}
	return triggers
}
