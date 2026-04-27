package crew

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/cli/crew/templates"
)

var hireNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// agentSchemaVersion is the schema_version string written into the scaffolded
// agent.yaml. Kept in sync with addons/crew/internal/crew.SchemaVersion.
// Duplicated intentionally to respect the core↔addon import boundary.
const agentSchemaVersion = "1"

type hireFlags struct {
	backend string
	mode    string
	force   bool
	from    string
}

func newHireCmd() *cobra.Command {
	var f hireFlags
	cmd := &cobra.Command{
		Use:   "hire <name>",
		Short: "Scaffold a new AI agent definition",
		Long: `Creates a new AI agent under ~/.shipyard/crew/<name>/ with starter
agent.yaml and prompt.md files. The agent is inert until you register it
with the running daemon via "shipyard crew apply". Use --backend to choose
between the Claude CLI and the Anthropic API, and --mode for on-demand or
persistent-service execution.`,
		Args:    cobra.ExactArgs(1),
		PreRunE: requireInstalled,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHire(cmd.OutOrStdout(), args[0], f)
		},
	}
	cmd.Flags().StringVar(&f.backend, "backend", "cli", "backend type: cli | anthropic_api")
	cmd.Flags().StringVar(&f.mode, "mode", "on-demand", "execution mode: on-demand | service")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite existing crew member")
	cmd.Flags().StringVar(&f.from, "from", "", "(reserved) template id to bootstrap from (noop in v1)")
	return cmd
}

func runHire(out io.Writer, name string, f hireFlags) error {
	if !hireNameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match %s", name, hireNameRe.String())
	}
	if f.backend != "cli" && f.backend != "anthropic_api" {
		return fmt.Errorf("invalid --backend %q: must be \"cli\" or \"anthropic_api\"", f.backend)
	}
	if f.mode != "on-demand" && f.mode != "service" {
		return fmt.Errorf("invalid --mode %q: must be \"on-demand\" or \"service\"", f.mode)
	}

	home, err := shipyardHome()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "crew", name)

	if _, err := os.Stat(dir); err == nil {
		if !f.force {
			return fmt.Errorf("crew member %q already exists at %s (use --force to overwrite)", name, dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove existing: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", dir, err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	data := struct {
		Name          string
		BackendType   string
		Mode          string
		SchemaVersion string
	}{
		Name:          name,
		BackendType:   f.backend,
		Mode:          f.mode,
		SchemaVersion: agentSchemaVersion,
	}

	if err := renderTemplate("agent.yaml.tmpl", filepath.Join(dir, "agent.yaml"), 0o600, data); err != nil {
		return err
	}
	if err := renderTemplate("prompt.md.tmpl", filepath.Join(dir, "prompt.md"), 0o600, data); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "memory", ".gitkeep"), nil, 0o600); err != nil {
		return fmt.Errorf("gitkeep: %w", err)
	}

	fmt.Fprintf(out, "Crew member %q created at %s\n", name, dir)
	fmt.Fprintf(out, "Next steps:\n  1. Edit %s\n  2. Declare tools in agent.yaml if needed\n  3. Run: shipyard crew run %s\n",
		filepath.Join(dir, "prompt.md"), name)
	return nil
}

func shipyardHome() (string, error) {
	if h := os.Getenv("SHIPYARD_HOME"); h != "" {
		return h, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(h, ".shipyard"), nil
}

func renderTemplate(name, dst string, mode os.FileMode, data any) error {
	raw, err := templates.FS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read template %s: %w", name, err)
	}
	tmpl, err := template.New(name).Parse(string(raw))
	if err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("open %s: %w", dst, err)
	}
	defer f.Close()
	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("execute %s: %w", name, err)
	}
	return nil
}
