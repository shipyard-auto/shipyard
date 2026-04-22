package crew

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/shipyard-auto/shipyard/internal/crewctl"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

// Exit codes produced by the `shipyard crew fire` command.
const (
	fireExitOK         = 0
	fireExitNotFound   = 1
	fireExitRemoveFail = 2
)

// fireAgentDoc is the tolerant subset of agent.yaml that fire needs. Parsing
// is deliberately lenient: unknown fields are ignored and missing fields
// default to safe values.
type fireAgentDoc struct {
	Execution struct {
		Mode string `yaml:"mode"`
	} `yaml:"execution"`
	Triggers []struct {
		Type     string `yaml:"type"`
		Schedule string `yaml:"schedule"`
		Route    string `yaml:"route"`
	} `yaml:"triggers"`
}

// fireDeps is the dependency injection struct used to make the fire command
// testable. Zero values are filled by withDefaults.
type fireDeps struct {
	Home   string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	IsTTY func() bool

	UnregisterService  func(ctx context.Context, name string) error
	UnreconcileCron    func(ctx context.Context, agentName string) error
	UnreconcileWebhook func(ctx context.Context, agentName, route string) error
}

func (d fireDeps) withDefaults() fireDeps {
	if d.Home == "" {
		if h, err := shipyardHome(); err == nil {
			d.Home = h
		}
	}
	if d.Stdin == nil {
		d.Stdin = os.Stdin
	}
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	if d.IsTTY == nil {
		d.IsTTY = func() bool { return tty.IsInteractive(tty.StdinFD()) }
	}
	if d.UnregisterService == nil {
		d.UnregisterService = defaultUnregisterService
	}
	if d.UnreconcileCron == nil {
		d.UnreconcileCron = defaultUnreconcileCron
	}
	if d.UnreconcileWebhook == nil {
		d.UnreconcileWebhook = defaultUnreconcileWebhook
	}
	return d
}

func newFireCmd() *cobra.Command {
	return newFireCmdWith(fireDeps{})
}

func newFireCmdWith(deps fireDeps) *cobra.Command {
	var yes, keepLogs bool
	cmd := &cobra.Command{
		Use:           "fire <name>",
		Short:         "Remove a crew member and clean up its triggers and service",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE:       requireInstalled,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := runFire(cmd.Context(), deps, args[0], yes, keepLogs)
			if code == fireExitOK {
				return nil
			}
			return &ExitError{Code: code}
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip interactive confirmation")
	cmd.Flags().BoolVar(&keepLogs, "keep-logs", false, "preserve logs directory")
	return cmd
}

func runFire(ctx context.Context, deps fireDeps, name string, yes, keepLogs bool) int {
	deps = deps.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}

	if !hireNameRe.MatchString(name) {
		fmt.Fprintf(deps.Stderr, "shipyard crew fire: invalid name %q\n", name)
		return fireExitNotFound
	}

	agentDir := filepath.Join(deps.Home, "crew", name)
	sockPath := filepath.Join(deps.Home, "run", "crew", name+".sock")
	pidPath := filepath.Join(deps.Home, "run", "crew", name+".pid")
	logsDir := filepath.Join(deps.Home, "logs", "crew", name)

	info, err := os.Stat(agentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(deps.Stderr, "shipyard crew fire: crew member not found: %s\n", name)
			return fireExitNotFound
		}
		fmt.Fprintf(deps.Stderr, "shipyard crew fire: stat %s: %s\n", agentDir, err)
		return fireExitNotFound
	}
	if !info.IsDir() {
		fmt.Fprintf(deps.Stderr, "shipyard crew fire: %s is not a directory\n", agentDir)
		return fireExitNotFound
	}

	doc, loadWarn := loadFireDoc(agentDir)
	if loadWarn != nil {
		fmt.Fprintf(deps.Stderr, "warning: %s\n", loadWarn)
	}

	if !yes {
		if !deps.IsTTY() {
			fmt.Fprintln(deps.Stderr, "shipyard crew fire: refusing to fire non-interactively without --yes")
			return fireExitNotFound
		}
		if !confirmFire(deps.Stdin, deps.Stdout, name) {
			fmt.Fprintln(deps.Stdout, "cancelled.")
			return fireExitOK
		}
	}

	if doc.Execution.Mode == ExecutionModeService {
		if err := deps.UnregisterService(ctx, name); err != nil {
			fmt.Fprintf(deps.Stderr, "warning: unregister service for %q: %s\n", name, err)
		}
	}

	// Cron cleanup is agent-wide and yaml-independent: it discovers every
	// `shipyard cron` entry whose Name matches the "crew:<agent>:" prefix
	// and deletes it. This survives out-of-band edits of agent.yaml between
	// the last `apply` and `fire` (see roadmap §1.6).
	if err := deps.UnreconcileCron(ctx, name); err != nil {
		fmt.Fprintf(deps.Stderr, "warning: unreconcile cron for %q: %s\n", name, err)
	}

	for _, t := range doc.Triggers {
		if t.Type != "webhook" || t.Route == "" {
			continue
		}
		if err := deps.UnreconcileWebhook(ctx, name, t.Route); err != nil {
			fmt.Fprintf(deps.Stderr, "warning: unreconcile webhook (%s) for %q: %s\n", t.Route, name, err)
		}
	}

	for _, p := range []string{sockPath, pidPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(deps.Stderr, "warning: remove %s: %s\n", p, err)
		}
	}

	if err := os.RemoveAll(agentDir); err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard crew fire: remove %s: %s\n", agentDir, err)
		return fireExitRemoveFail
	}

	if !keepLogs {
		if err := os.RemoveAll(logsDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(deps.Stderr, "warning: remove logs %s: %s\n", logsDir, err)
		}
	}

	fmt.Fprintf(deps.Stdout, "Crew member %q has been fired.\n", name)
	return fireExitOK
}

func loadFireDoc(agentDir string) (fireAgentDoc, error) {
	var doc fireAgentDoc
	raw, err := os.ReadFile(filepath.Join(agentDir, "agent.yaml"))
	if err != nil {
		return doc, fmt.Errorf("read agent.yaml: %w (continuing with defaults)", err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fireAgentDoc{}, fmt.Errorf("parse agent.yaml: %w (continuing with defaults)", err)
	}
	return doc, nil
}

func confirmFire(stdin io.Reader, stdout io.Writer, name string) bool {
	fmt.Fprintf(stdout, "Fire crew member '%s' and remove its data? [y/N] ", name)
	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	answer := strings.TrimSpace(line)
	return answer == "y" || answer == "Y"
}

// cronListEntry mirrors the minimal subset of `shipyard cron list --json`
// needed to discover crew-owned entries (matched by the Name prefix).
type cronListEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// cronNamePrefixFor builds the Name prefix that identifies cron entries
// owned by agentName, per task 28a. The trailing ":" is load-bearing — it
// prevents HasPrefix("crew:foo:", ...) from matching "crew:foo-bar:...".
func cronNamePrefixFor(agentName string) string {
	return "crew:" + agentName + ":"
}

func defaultUnregisterService(ctx context.Context, name string) error {
	m, err := crewctl.NewManager()
	if err != nil {
		return err
	}
	return m.UnregisterAgentService(ctx, name)
}

// defaultUnreconcileCron lists every `shipyard cron` entry, keeps the ones
// whose Name starts with "crew:<agentName>:" and deletes each. It is
// idempotent: a second invocation finds no matches and returns nil.
//
// Delete errors are collected but do not short-circuit the loop, so a
// transient failure on one entry does not strand the rest.
func defaultUnreconcileCron(ctx context.Context, agentName string) error {
	listCmd := exec.CommandContext(ctx, "shipyard", "cron", "list", "--json")
	out, err := listCmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("cron list: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("cron list: %w", err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var entries []cronListEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return fmt.Errorf("cron list parse: %w", err)
	}
	prefix := cronNamePrefixFor(agentName)
	var firstErr error
	for _, e := range entries {
		if !strings.HasPrefix(e.Name, prefix) {
			continue
		}
		if e.ID == "" {
			continue
		}
		delCmd := exec.CommandContext(ctx, "shipyard", "cron", "delete", e.ID)
		if out, err := delCmd.CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("cron delete %s (%s): %w: %s", e.ID, e.Name, err, strings.TrimSpace(string(out)))
		}
	}
	return firstErr
}

// defaultUnreconcileWebhook deletes a fairway route. --yes is required
// because the core CLI refuses to delete interactively without confirmation
// and the addon always runs non-interactively.
func defaultUnreconcileWebhook(ctx context.Context, agentName, route string) error {
	_ = agentName
	cmd := exec.CommandContext(ctx, "shipyard", "fairway", "route", "delete", route, "--yes")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
