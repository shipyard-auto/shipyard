package crew

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// listEntry is the row shape used for both JSON and tabular output.
type listEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Backend     string   `json:"backend"`
	Mode        string   `json:"mode"`
	Pool        string   `json:"pool"`
	Triggers    []string `json:"triggers"`
	State       string   `json:"state"`
}

// listAgentDoc is the tolerant subset of agent.yaml consumed by list.
// The core MUST NOT import addons/crew/internal/*; we mirror the handful of
// fields we need here.
type listAgentDoc struct {
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

// listDeps is the dependency-injection struct for the list command.
type listDeps struct {
	Home    string
	Stdout  io.Writer
	Stderr  io.Writer
	Verbose bool
	// IsAlive reports whether a given PID is live. Overridable for tests.
	IsAlive func(pid int) bool
}

func (d listDeps) withDefaults() listDeps {
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	if d.IsAlive == nil {
		d.IsAlive = pidAlive
	}
	return d
}

type listFlags struct {
	JSON    bool
	Long    bool
	Verbose bool
}

func newListCmd() *cobra.Command {
	return newListCmdWith(listDeps{})
}

func newListCmdWith(deps listDeps) *cobra.Command {
	flags := &listFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered crew members",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := deps
			d.Verbose = flags.Verbose
			return runList(d, *flags)
		},
	}
	cmd.Flags().BoolVar(&flags.JSON, "json", false, "emit output as JSON")
	cmd.Flags().BoolVar(&flags.Long, "long", false, "include DESCRIPTION column")
	cmd.Flags().BoolVarP(&flags.Verbose, "verbose", "v", false, "warn when a directory is skipped")
	return cmd
}

func runList(deps listDeps, flags listFlags) error {
	deps = deps.withDefaults()

	home := deps.Home
	if home == "" {
		h, err := shipyardHome()
		if err != nil {
			return err
		}
		home = h
	}

	crewDir := filepath.Join(home, "crew")
	entries, err := os.ReadDir(crewDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return emitList(deps, nil, flags)
		}
		return fmt.Errorf("read %s: %w", crewDir, err)
	}

	results := make([]listEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		agentPath := filepath.Join(crewDir, e.Name(), "agent.yaml")
		raw, err := os.ReadFile(agentPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if deps.Verbose {
					fmt.Fprintf(deps.Stderr, "skip %s: no agent.yaml\n", e.Name())
				}
				continue
			}
			if deps.Verbose {
				fmt.Fprintf(deps.Stderr, "skip %s: %s\n", e.Name(), err)
			}
			continue
		}
		var doc listAgentDoc
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			if deps.Verbose {
				fmt.Fprintf(deps.Stderr, "skip %s: parse agent.yaml: %s\n", e.Name(), err)
			}
			continue
		}
		name := doc.Name
		if name == "" {
			name = e.Name()
		}

		entry := listEntry{
			Name:        name,
			Description: doc.Description,
			Backend:     doc.Backend.Type,
			Mode:        doc.Execution.Mode,
			Pool:        doc.Execution.Pool,
			Triggers:    distinctTriggerTypes(doc),
			State:       resolveState(home, name, doc.Execution.Mode, deps.IsAlive),
		}
		results = append(results, entry)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return emitList(deps, results, flags)
}

func distinctTriggerTypes(doc listAgentDoc) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(doc.Triggers))
	for _, t := range doc.Triggers {
		if t.Type == "" {
			continue
		}
		if _, dup := seen[t.Type]; dup {
			continue
		}
		seen[t.Type] = struct{}{}
		out = append(out, t.Type)
	}
	return out
}

// resolveState determines the runtime state for an agent entry.
//   - service + live PID file → "running"
//   - service + missing/dead  → "stopped"
//   - anything else           → "-"
func resolveState(home, name, mode string, isAlive func(int) bool) string {
	if mode != ExecutionModeService {
		return "-"
	}
	pidPath := filepath.Join(home, "run", "crew", name+".pid")
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return "stopped"
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return "stopped"
	}
	if isAlive(pid) {
		return "running"
	}
	return "stopped"
}

// pidAlive reports whether a process with the given PID is currently live,
// using the POSIX `kill -0` probe. Windows is not supported in v1.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

func emitList(deps listDeps, entries []listEntry, flags listFlags) error {
	if flags.JSON {
		if entries == nil {
			entries = []listEntry{}
		}
		enc := json.NewEncoder(deps.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(deps.Stdout, "no crew members")
		return nil
	}
	tw := tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	if flags.Long {
		fmt.Fprintln(tw, "NAME\tBACKEND\tMODE\tPOOL\tTRIGGERS\tSTATE\tDESCRIPTION")
	} else {
		fmt.Fprintln(tw, "NAME\tBACKEND\tMODE\tPOOL\tTRIGGERS\tSTATE")
	}
	for _, e := range entries {
		triggers := strings.Join(e.Triggers, ",")
		if triggers == "" {
			triggers = "-"
		}
		backend := defaultStr(e.Backend, "-")
		mode := defaultStr(e.Mode, "-")
		pool := defaultStr(e.Pool, "-")
		state := defaultStr(e.State, "-")
		if flags.Long {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, backend, mode, pool, triggers, state, e.Description)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, backend, mode, pool, triggers, state)
		}
	}
	return tw.Flush()
}

func defaultStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
