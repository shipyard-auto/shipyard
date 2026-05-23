package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// WebhookSchemaVersion is the current schema version of `.reconciled.json`.
// Unknown versions cause the reconciler to refuse to operate so the operator
// is forced to migrate explicitly.
const WebhookSchemaVersion = 1

// WebhookReconciler reconciles `webhook` triggers declared in an agent.yaml
// with routes registered in the fairway addon. It keeps a local snapshot in
// ~/.shipyard/crew/<name>/.reconciled.json so removal is idempotent even
// across processes. Reconcilers are NOT goroutine-safe across agents.
type WebhookReconciler struct {
	Runner  CommandRunner
	Home    string // override ~/.shipyard root; empty means $HOME/.shipyard
	NowFunc func() time.Time
}

// NewWebhookReconciler returns a reconciler backed by runner. home overrides
// the shipyard root (pass "" for the default). If runner is nil, ExecRunner
// is used.
func NewWebhookReconciler(runner CommandRunner, home string) *WebhookReconciler {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &WebhookReconciler{Runner: runner, Home: home, NowFunc: time.Now}
}

// WebhookChange describes a single route state change.
type WebhookChange struct {
	Route string `json:"route"`
}

// webhookDesired is the in-memory representation of one desired webhook
// trigger after extraction from agent.yaml. It carries the auth fields so the
// diff can detect changes that require a fairway re-add.
type webhookDesired struct {
	Route     string
	Auth      string
	AuthToken string
}

// WebhookDiff is the result of comparing desired vs current webhook state.
// Sorted lists for determinism.
type WebhookDiff struct {
	Added     []WebhookChange `json:"added"`
	Removed   []WebhookChange `json:"removed"`
	Unchanged []WebhookChange `json:"unchanged"`
}

type reconciledWebhook struct {
	Route        string    `json:"route"`
	RegisteredAt time.Time `json:"registered_at"`
	// Auth and AuthToken are persisted so the reconciler can detect when an
	// agent.yaml changes its auth setting and trigger a re-add. Pre-auth
	// snapshots have these as empty strings and are treated as "different"
	// from any non-empty desired auth, which forces a clean re-registration.
	Auth      string `json:"auth,omitempty"`
	AuthToken string `json:"auth_token,omitempty"`
}

type reconciledFile struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Webhooks      []reconciledWebhook `json:"webhooks"`
}

// Plan computes the webhook diff between agent.yaml and the local snapshot
// without touching fairway. Used for --dry-run.
func (r *WebhookReconciler) Plan(ctx context.Context, agent *crew.Agent) (WebhookDiff, error) {
	_ = ctx
	if agent == nil {
		return WebhookDiff{}, fmt.Errorf("webhook plan: nil agent")
	}
	desired, err := r.desired(agent)
	if err != nil {
		return WebhookDiff{}, err
	}
	current, err := r.loadState(agent.Name)
	if err != nil {
		return WebhookDiff{}, err
	}
	return diffWebhook(desired, current), nil
}

// Reconcile ensures the fairway addon has a route registered for every
// `webhook` trigger in agent and nothing else. The local snapshot file is
// rewritten atomically so a crash mid-reconciliation never corrupts state.
func (r *WebhookReconciler) Reconcile(ctx context.Context, agent *crew.Agent) (WebhookDiff, error) {
	if agent == nil {
		return WebhookDiff{}, fmt.Errorf("webhook reconcile: nil agent")
	}
	desired, err := r.desired(agent)
	if err != nil {
		return WebhookDiff{}, err
	}
	current, err := r.loadState(agent.Name)
	if err != nil {
		return WebhookDiff{}, err
	}
	diff := diffWebhook(desired, current)

	desiredByRoute := map[string]webhookDesired{}
	for _, d := range desired {
		desiredByRoute[d.Route] = d
	}

	// Removed routes go first so that re-added entries (auth change on the
	// same route) hit fairway as delete-then-add and don't trip the
	// "route already exists" error.
	for _, c := range diff.Removed {
		if err := r.runFairwayDelete(ctx, c.Route); err != nil {
			if !isNotFound(err) {
				return WebhookDiff{}, fmt.Errorf("webhook reconcile: delete %s: %w", c.Route, err)
			}
		}
	}
	for _, c := range diff.Added {
		d := desiredByRoute[c.Route]
		if err := r.runFairwayAdd(ctx, d, agent.Name); err != nil {
			return WebhookDiff{}, fmt.Errorf("webhook reconcile: add %s: %w", c.Route, err)
		}
	}

	now := r.now()
	currentByRoute := map[string]reconciledWebhook{}
	for _, wh := range current.Webhooks {
		currentByRoute[wh.Route] = wh
	}
	next := reconciledFile{SchemaVersion: WebhookSchemaVersion}
	for _, d := range desired {
		registered := now
		if existing, ok := currentByRoute[d.Route]; ok && existing.Auth == d.Auth && existing.AuthToken == d.AuthToken {
			// Same route AND same auth → preserve the original timestamp.
			// A changed auth produces a fresh registration.
			registered = existing.RegisteredAt
		}
		next.Webhooks = append(next.Webhooks, reconciledWebhook{
			Route:        d.Route,
			RegisteredAt: registered,
			Auth:         d.Auth,
			AuthToken:    d.AuthToken,
		})
	}
	if err := r.saveState(agent.Name, next); err != nil {
		return WebhookDiff{}, err
	}
	return diff, nil
}

// Unreconcile removes every webhook previously reconciled for agent and then
// deletes the snapshot file. Missing-route errors from fairway are tolerated.
func (r *WebhookReconciler) Unreconcile(ctx context.Context, agent *crew.Agent) error {
	if agent == nil {
		return fmt.Errorf("webhook unreconcile: nil agent")
	}
	current, err := r.loadState(agent.Name)
	if err != nil {
		return err
	}
	if len(current.Webhooks) == 0 {
		return r.removeStateFile(agent.Name)
	}
	sort.Slice(current.Webhooks, func(i, j int) bool {
		return current.Webhooks[i].Route < current.Webhooks[j].Route
	})
	for _, wh := range current.Webhooks {
		if err := r.runFairwayDelete(ctx, wh.Route); err != nil {
			if isNotFound(err) {
				continue
			}
			return fmt.Errorf("webhook unreconcile: delete %s: %w", wh.Route, err)
		}
	}
	return r.removeStateFile(agent.Name)
}

// desired extracts and validates the webhook triggers from agent. Returns
// routes paired with their auth configuration, sorted by route for
// deterministic output.
func (r *WebhookReconciler) desired(agent *crew.Agent) ([]webhookDesired, error) {
	var entries []webhookDesired
	seen := map[string]struct{}{}
	for _, t := range agent.Triggers {
		if t.Type != crew.TriggerWebhook {
			continue
		}
		if strings.TrimSpace(t.Route) == "" {
			return nil, fmt.Errorf("invalid webhook trigger for agent %q: empty route", agent.Name)
		}
		if !strings.HasPrefix(t.Route, "/") {
			return nil, fmt.Errorf("invalid webhook trigger for agent %q: route %q must start with \"/\"", agent.Name, t.Route)
		}
		if _, dup := seen[t.Route]; dup {
			return nil, fmt.Errorf("duplicate webhook route %q in agent %q", t.Route, agent.Name)
		}
		switch t.Auth {
		case "bearer":
			if strings.TrimSpace(t.AuthToken) == "" {
				return nil, fmt.Errorf("invalid webhook trigger for agent %q: auth=bearer requires auth_token", agent.Name)
			}
		case "local-only":
			if t.AuthToken != "" {
				return nil, fmt.Errorf("invalid webhook trigger for agent %q: auth=local-only must not set auth_token", agent.Name)
			}
		case "":
			return nil, fmt.Errorf("invalid webhook trigger for agent %q: route %q requires auth: one of [bearer, local-only]", agent.Name, t.Route)
		default:
			return nil, fmt.Errorf("invalid webhook trigger for agent %q: auth %q must be one of [bearer, local-only] (token type not yet supported via crew triggers)", agent.Name, t.Auth)
		}
		seen[t.Route] = struct{}{}
		entries = append(entries, webhookDesired{
			Route:     t.Route,
			Auth:      t.Auth,
			AuthToken: t.AuthToken,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Route < entries[j].Route })
	return entries, nil
}

func (r *WebhookReconciler) runFairwayAdd(ctx context.Context, d webhookDesired, agentName string) error {
	args := []string{
		"fairway", "route", "add",
		"--path", d.Route,
		"--action", "crew.run",
		"--target", agentName,
	}
	if d.Auth != "" {
		args = append(args, "--auth", d.Auth)
	}
	if d.Auth == "bearer" && d.AuthToken != "" {
		args = append(args, "--auth-token", d.AuthToken)
	}
	_, err := r.Runner.Run(ctx, "shipyard", args...)
	return err
}

// runFairwayDelete removes a route. --yes is required because the core CLI
// refuses to delete interactively without confirmation, and the addon
// always runs non-interactively.
func (r *WebhookReconciler) runFairwayDelete(ctx context.Context, route string) error {
	_, err := r.Runner.Run(ctx, "shipyard", "fairway", "route", "delete", route, "--yes")
	return err
}

// diffWebhook compares desired triggers vs current snapshot. A route in both
// sets is "unchanged" only when its auth configuration also matches;
// otherwise it appears in both Removed (current entry to drop) and Added
// (new desired entry), forcing a clean re-registration in fairway.
func diffWebhook(desired []webhookDesired, current reconciledFile) WebhookDiff {
	currentByRoute := map[string]reconciledWebhook{}
	for _, wh := range current.Webhooks {
		currentByRoute[wh.Route] = wh
	}
	desiredSet := map[string]struct{}{}
	for _, d := range desired {
		desiredSet[d.Route] = struct{}{}
	}

	var diff WebhookDiff
	for _, d := range desired {
		existing, ok := currentByRoute[d.Route]
		switch {
		case !ok:
			diff.Added = append(diff.Added, WebhookChange{Route: d.Route})
		case existing.Auth != d.Auth || existing.AuthToken != d.AuthToken:
			diff.Removed = append(diff.Removed, WebhookChange{Route: d.Route})
			diff.Added = append(diff.Added, WebhookChange{Route: d.Route})
		default:
			diff.Unchanged = append(diff.Unchanged, WebhookChange{Route: d.Route})
		}
	}
	var removedRoutes []string
	for route := range currentByRoute {
		if _, ok := desiredSet[route]; !ok {
			removedRoutes = append(removedRoutes, route)
		}
	}
	sort.Strings(removedRoutes)
	for _, route := range removedRoutes {
		diff.Removed = append(diff.Removed, WebhookChange{Route: route})
	}
	sort.SliceStable(diff.Added, func(i, j int) bool { return diff.Added[i].Route < diff.Added[j].Route })
	sort.SliceStable(diff.Removed, func(i, j int) bool { return diff.Removed[i].Route < diff.Removed[j].Route })
	sort.SliceStable(diff.Unchanged, func(i, j int) bool { return diff.Unchanged[i].Route < diff.Unchanged[j].Route })
	return diff
}

func (r *WebhookReconciler) loadState(agentName string) (reconciledFile, error) {
	path := r.statePath(agentName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return reconciledFile{SchemaVersion: WebhookSchemaVersion}, nil
		}
		return reconciledFile{}, fmt.Errorf("webhook state read %s: %w", path, err)
	}
	var doc reconciledFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return reconciledFile{}, fmt.Errorf("webhook state parse %s: %w", path, err)
	}
	if doc.SchemaVersion != WebhookSchemaVersion {
		return reconciledFile{}, fmt.Errorf("webhook state %s: unsupported schemaVersion %d (expected %d); manual migration required", path, doc.SchemaVersion, WebhookSchemaVersion)
	}
	return doc, nil
}

// saveState writes the snapshot atomically: write to .tmp, then os.Rename.
// A crash between Write and Rename leaves the previous file untouched.
func (r *WebhookReconciler) saveState(agentName string, doc reconciledFile) error {
	path := r.statePath(agentName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("webhook state mkdir: %w", err)
	}
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("webhook state marshal: %w", err)
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("webhook state write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("webhook state rename: %w", err)
	}
	return nil
}

func (r *WebhookReconciler) removeStateFile(agentName string) error {
	path := r.statePath(agentName)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("webhook state remove: %w", err)
	}
	return nil
}

func (r *WebhookReconciler) statePath(agentName string) string {
	return filepath.Join(r.home(), "crew", agentName, ".reconciled.json")
}

func (r *WebhookReconciler) home() string {
	if r.Home != "" {
		return r.Home
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".shipyard"
	}
	return filepath.Join(h, ".shipyard")
}

func (r *WebhookReconciler) now() time.Time {
	if r.NowFunc != nil {
		return r.NowFunc()
	}
	return time.Now()
}
