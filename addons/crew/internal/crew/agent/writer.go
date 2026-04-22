package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

func Write(a *crew.Agent) error {
	if a == nil {
		return errors.New("write agent: nil agent")
	}
	if a.Dir == "" {
		return errors.New("write agent: empty Dir")
	}
	if err := a.Validate(); err != nil {
		return fmt.Errorf("write agent %s: validate: %w", a.Dir, err)
	}
	if err := os.MkdirAll(a.Dir, 0o700); err != nil {
		return fmt.Errorf("write agent %s: mkdir: %w", a.Dir, err)
	}
	doc := yamlDoc{
		SchemaVersion: crew.SchemaVersion,
		Name:          a.Name,
		Description:   a.Description,
		Backend:       a.Backend,
		Execution:     a.Execution,
		Conversation:  a.Conversation,
		Triggers:      a.Triggers,
		Tools:         a.Tools,
	}
	buf, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("write agent %s: marshal: %w", a.Dir, err)
	}
	finalPath := filepath.Join(a.Dir, "agent.yaml")
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, buf, 0o600); err != nil {
		return fmt.Errorf("write agent %s: write tmp: %w", a.Dir, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write agent %s: rename: %w", a.Dir, err)
	}
	return nil
}
