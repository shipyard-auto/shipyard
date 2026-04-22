package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// CronReconciler reconciles `cron` triggers declared in an agent.yaml with
// entries registered in the core `shipyard cron` subsystem. Reconcilers are
// NOT goroutine-safe across different agents; the caller must serialise
// concurrent calls.
//
// Naming convention (see roadmap.md §1.6): crew-owned cron entries are
// identified by their Name field, formatted as "crew:<agent>:<idx>", where
// <idx> is the trigger's index in the agent.yaml `triggers:` array (0-based,
// across all trigger types). The ":" delimiter is safe because the agent
// name regex forbids it.
type CronReconciler struct {
	Runner CommandRunner
}

// NewCronReconciler returns a reconciler backed by the given runner. If
// runner is nil, ExecRunner is used.
func NewCronReconciler(runner CommandRunner) *CronReconciler {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &CronReconciler{Runner: runner}
}

// CronEntry mirrors the subset of `shipyard cron list --json` we care about.
type CronEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
}

// CronChange describes a single trigger state change produced by Plan or
// Reconcile. ID is present for entries that exist in the current state; for
// newly added entries it is populated after a post-apply re-list.
type CronChange struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule,omitempty"`
	ID       string `json:"id,omitempty"`
}

// CronDiff is the result of comparing desired (agent.yaml) vs current
// (shipyard cron list) state. Sorted lists so the output is deterministic.
type CronDiff struct {
	Added     []CronChange `json:"added"`
	Removed   []CronChange `json:"removed"`
	Unchanged []CronChange `json:"unchanged"`
}

// CronNamePrefix is the Name prefix used to identify crew-owned cron
// entries for agentName. Always ends with ":" so HasPrefix matches never
// leak into neighbouring agent names.
func CronNamePrefix(agentName string) string {
	return "crew:" + agentName + ":"
}

// CronName returns the Name value used in `shipyard cron` for the idx-th
// trigger of agentName (0-based, index in the full `triggers:` array).
func CronName(agentName string, idx int) string {
	return CronNamePrefix(agentName) + strconv.Itoa(idx)
}

// Plan computes the diff between desired and current state without applying
// any change. Useful for --dry-run.
func (r *CronReconciler) Plan(ctx context.Context, agent *crew.Agent) (CronDiff, error) {
	if agent == nil {
		return CronDiff{}, fmt.Errorf("cron plan: nil agent")
	}
	desired, err := desiredCron(agent)
	if err != nil {
		return CronDiff{}, err
	}
	current, err := r.listForAgent(ctx, agent.Name)
	if err != nil {
		return CronDiff{}, err
	}
	return diffCron(desired, current), nil
}

// Reconcile computes the diff, applies it (delete then add, so a changed
// entry transitions cleanly through the random-id space), and returns the
// applied diff with post-apply ids populated on Added entries.
//
// Running Reconcile twice in a row must be idempotent: the second run
// observes every entry as unchanged and performs no side-effect.
func (r *CronReconciler) Reconcile(ctx context.Context, agent *crew.Agent) (CronDiff, error) {
	if agent == nil {
		return CronDiff{}, fmt.Errorf("cron reconcile: nil agent")
	}
	desired, err := desiredCron(agent)
	if err != nil {
		return CronDiff{}, err
	}
	current, err := r.listForAgent(ctx, agent.Name)
	if err != nil {
		return CronDiff{}, err
	}
	diff := diffCron(desired, current)

	for _, c := range diff.Removed {
		if c.ID == "" {
			continue
		}
		if _, err := r.Runner.Run(ctx, "shipyard", "cron", "delete", c.ID); err != nil {
			return CronDiff{}, fmt.Errorf("cron reconcile: delete %s (%s): %w", c.ID, c.Name, err)
		}
	}

	for _, c := range diff.Added {
		args := []string{
			"cron", "add",
			"--name", c.Name,
			"--schedule", c.Schedule,
			"--command", fmt.Sprintf("shipyard crew run %s", agent.Name),
		}
		if _, err := r.Runner.Run(ctx, "shipyard", args...); err != nil {
			return CronDiff{}, fmt.Errorf("cron reconcile: add %s: %w", c.Name, err)
		}
	}

	if len(diff.Added) > 0 {
		refreshed, err := r.listForAgent(ctx, agent.Name)
		if err != nil {
			return CronDiff{}, fmt.Errorf("cron reconcile: refresh ids: %w", err)
		}
		byName := map[string]CronEntry{}
		for _, e := range refreshed {
			byName[e.Name] = e
		}
		for i, c := range diff.Added {
			if e, ok := byName[c.Name]; ok {
				diff.Added[i].ID = e.ID
			}
		}
	}

	return diff, nil
}

// Unreconcile removes every cron entry whose Name starts with
// CronNamePrefix(agent.Name). Errors from `cron delete` on individual ids
// are propagated, but the loop continues so transient failures can be
// retried on a second invocation.
func (r *CronReconciler) Unreconcile(ctx context.Context, agent *crew.Agent) error {
	if agent == nil {
		return fmt.Errorf("cron unreconcile: nil agent")
	}
	entries, err := r.listForAgent(ctx, agent.Name)
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if _, err := r.Runner.Run(ctx, "shipyard", "cron", "delete", e.ID); err != nil {
			if isNotFound(err) {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("cron unreconcile: delete %s (%s): %w", e.ID, e.Name, err)
			}
		}
	}
	return firstErr
}

// listForAgent runs `shipyard cron list --json` and filters the result by
// CronNamePrefix(agentName). The filter is applied client-side because the
// core CLI does not expose server-side filtering.
func (r *CronReconciler) listForAgent(ctx context.Context, agentName string) ([]CronEntry, error) {
	out, err := r.Runner.Run(ctx, "shipyard", "cron", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("cron list: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var entries []CronEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("cron list parse: %w", err)
	}
	prefix := CronNamePrefix(agentName)
	filtered := make([]CronEntry, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name, prefix) {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// desiredCron builds the desired state map for cron triggers only. The key
// is the Name value ("crew:<agent>:<idx>"); the index is the position in
// the full triggers array (task 28a contract).
func desiredCron(agent *crew.Agent) (map[string]CronChange, error) {
	out := map[string]CronChange{}
	for idx, t := range agent.Triggers {
		if t.Type != crew.TriggerCron {
			continue
		}
		if strings.TrimSpace(t.Schedule) == "" {
			return nil, fmt.Errorf("invalid cron trigger for agent %q at index %d: empty schedule", agent.Name, idx)
		}
		name := CronName(agent.Name, idx)
		out[name] = CronChange{Name: name, Schedule: t.Schedule}
	}
	return out, nil
}

// diffCron compares desired vs current by Name. An entry whose schedule
// differs is returned in both Removed (old id) and Added (new entry),
// because `shipyard cron` does not expose in-place update by Name (ids are
// random and re-adding produces a new one). Sorted by Name.
func diffCron(desired map[string]CronChange, current []CronEntry) CronDiff {
	currentByName := map[string]CronEntry{}
	for _, e := range current {
		currentByName[e.Name] = e
	}

	var diff CronDiff
	for _, name := range sortedNames(desired) {
		want := desired[name]
		have, ok := currentByName[name]
		if !ok {
			diff.Added = append(diff.Added, want)
			continue
		}
		if have.Schedule == want.Schedule {
			diff.Unchanged = append(diff.Unchanged, CronChange{
				Name:     name,
				Schedule: want.Schedule,
				ID:       have.ID,
			})
			continue
		}
		diff.Removed = append(diff.Removed, CronChange{
			Name:     name,
			Schedule: have.Schedule,
			ID:       have.ID,
		})
		diff.Added = append(diff.Added, want)
	}

	var currentNames []string
	for name := range currentByName {
		if _, kept := desired[name]; kept {
			continue
		}
		currentNames = append(currentNames, name)
	}
	sort.Strings(currentNames)
	for _, name := range currentNames {
		have := currentByName[name]
		diff.Removed = append(diff.Removed, CronChange{
			Name:     name,
			Schedule: have.Schedule,
			ID:       have.ID,
		})
	}

	sort.SliceStable(diff.Added, func(i, j int) bool { return diff.Added[i].Name < diff.Added[j].Name })
	sort.SliceStable(diff.Removed, func(i, j int) bool { return diff.Removed[i].Name < diff.Removed[j].Name })
	sort.SliceStable(diff.Unchanged, func(i, j int) bool { return diff.Unchanged[i].Name < diff.Unchanged[j].Name })
	return diff
}

func sortedNames(m map[string]CronChange) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isNotFound classifies whether a runner error represents a "not found"
// response from `shipyard cron delete`. The shipyard cron subsystem does
// not expose typed errors through the subprocess boundary yet, so we match
// on stderr substrings.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "was not found")
}
