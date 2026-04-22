package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

func TestWebhookReconcileAddsRoutesAndPersistsState(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeRunner{}
	r := NewWebhookReconciler(runner, home)
	r.NowFunc = func() time.Time { return time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC) }

	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerWebhook, Route: "/a"},
			{Type: crew.TriggerWebhook, Route: "/b"},
			{Type: crew.TriggerCron, Schedule: "* * * * *"}, // ignored
		},
	}
	diff, err := r.Reconcile(context.Background(), agent)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(diff.Added) != 2 {
		t.Fatalf("want 2 added, got %+v", diff.Added)
	}
	if got := len(runner.callsWith("fairway route add")); got != 2 {
		t.Fatalf("want 2 adds, got %d", got)
	}
	for _, c := range runner.callsWith("fairway route add") {
		joined := strings.Join(c.Args, " ")
		if !strings.Contains(joined, "--action crew.run") {
			t.Fatalf("expected --action crew.run in: %s", joined)
		}
		if !strings.Contains(joined, "--target demo") {
			t.Fatalf("expected --target demo in: %s", joined)
		}
	}
	statePath := filepath.Join(home, "crew", "demo", ".reconciled.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	var doc reconciledFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("state unmarshal: %v", err)
	}
	if doc.SchemaVersion != WebhookSchemaVersion {
		t.Fatalf("bad schemaVersion: %+v", doc)
	}
	if len(doc.Webhooks) != 2 {
		t.Fatalf("want 2 webhooks, got %d", len(doc.Webhooks))
	}
}

func TestWebhookReconcileIdempotent(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeRunner{}
	r := NewWebhookReconciler(runner, home)

	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerWebhook, Route: "/a"},
		},
	}
	diff, err := r.Reconcile(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Added) != 1 {
		t.Fatalf("first run: want 1 added, got %+v", diff.Added)
	}
	firstAdds := len(runner.callsWith("fairway route add"))
	diff2, err := r.Reconcile(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff2.Added) != 0 || len(diff2.Removed) != 0 {
		t.Fatalf("second run must be noop: %+v", diff2)
	}
	if len(diff2.Unchanged) != 1 {
		t.Fatalf("second run: want 1 unchanged, got %+v", diff2.Unchanged)
	}
	if got := len(runner.callsWith("fairway route add")); got != firstAdds {
		t.Fatalf("second reconcile must not re-add; before=%d after=%d", firstAdds, got)
	}
	if got := len(runner.callsWith("fairway route delete")); got != 0 {
		t.Fatalf("expected 0 deletes, got %d", got)
	}
}

func TestWebhookReconcileRemovesStaleRoute(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeRunner{}
	r := NewWebhookReconciler(runner, home)

	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerWebhook, Route: "/a"},
			{Type: crew.TriggerWebhook, Route: "/b"},
		},
	}
	if _, err := r.Reconcile(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	agent.Triggers = agent.Triggers[:1]
	diff, err := r.Reconcile(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].Route != "/b" {
		t.Fatalf("Removed unexpected: %+v", diff.Removed)
	}
	deletes := runner.callsWith("fairway route delete")
	if len(deletes) != 1 {
		t.Fatalf("want 1 delete, got %d", len(deletes))
	}
	joined := strings.Join(deletes[0].Args, " ")
	if !strings.Contains(joined, "/b") {
		t.Fatalf("expected deletion of /b: %s", joined)
	}
	if !strings.Contains(joined, "--yes") {
		t.Fatalf("delete must pass --yes for non-interactive core CLI: %s", joined)
	}
	raw, err := os.ReadFile(filepath.Join(home, "crew", "demo", ".reconciled.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc reconciledFile
	_ = json.Unmarshal(raw, &doc)
	for _, wh := range doc.Webhooks {
		if wh.Route == "/b" {
			t.Fatalf("/b should be gone from state")
		}
	}
}

func TestWebhookUnreconcileRemovesAllAndDeletesStateFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeRunner{}
	r := NewWebhookReconciler(runner, home)

	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerWebhook, Route: "/a"},
			{Type: crew.TriggerWebhook, Route: "/b"},
		},
	}
	if _, err := r.Reconcile(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if err := r.Unreconcile(context.Background(), agent); err != nil {
		t.Fatalf("unreconcile: %v", err)
	}
	if got := len(runner.callsWith("fairway route delete")); got != 2 {
		t.Fatalf("want 2 deletes, got %d", got)
	}
	if _, err := os.Stat(filepath.Join(home, "crew", "demo", ".reconciled.json")); !os.IsNotExist(err) {
		t.Fatalf("state file must be deleted, stat err=%v", err)
	}
}

func TestWebhookUnreconcileNoStateIsNoop(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeRunner{}
	r := NewWebhookReconciler(runner, home)
	agent := &crew.Agent{Name: "demo"}
	if err := r.Unreconcile(context.Background(), agent); err != nil {
		t.Fatalf("unreconcile: %v", err)
	}
	if len(runner.callsWith("fairway route delete")) != 0 {
		t.Fatal("no delete calls expected")
	}
}

func TestWebhookUnreconcileIgnoresNotFound(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeRunner{}
	r := NewWebhookReconciler(runner, home)
	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerWebhook, Route: "/a"},
		},
	}
	if _, err := r.Reconcile(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	r.Runner = &fakeRunner{
		responses: []fakeResponse{
			{Match: matchArgsContain("fairway", "route", "delete"), Err: errors.New("fairway: route was not found")},
		},
	}
	if err := r.Unreconcile(context.Background(), agent); err != nil {
		t.Fatalf("unreconcile: %v", err)
	}
}

func TestWebhookReconcileValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		trigger crew.Trigger
		wantErr string
	}{
		{"empty route", crew.Trigger{Type: crew.TriggerWebhook, Route: "   "}, "empty route"},
		{"missing slash", crew.Trigger{Type: crew.TriggerWebhook, Route: "noslash"}, `must start with "/"`},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			r := NewWebhookReconciler(&fakeRunner{}, home)
			_, err := r.Reconcile(context.Background(), &crew.Agent{Name: "demo", Triggers: []crew.Trigger{tc.trigger}})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got err=%v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

func TestWebhookReconcileDuplicateRoute(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := NewWebhookReconciler(&fakeRunner{}, home)
	_, err := r.Reconcile(context.Background(), &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerWebhook, Route: "/a"},
			{Type: crew.TriggerWebhook, Route: "/a"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate webhook route") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestWebhookReconcileUnknownSchemaVersion(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "crew", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".reconciled.json"), []byte(`{"schemaVersion":99,"webhooks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewWebhookReconciler(&fakeRunner{}, home)
	_, err := r.Reconcile(context.Background(), &crew.Agent{Name: "demo"})
	if err == nil || !strings.Contains(err.Error(), "schemaVersion") {
		t.Fatalf("expected schemaVersion error, got %v", err)
	}
}

func TestWebhookReconcileAtomicWritePreservesOriginalOnRenameFailure(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := NewWebhookReconciler(&fakeRunner{}, home)
	agent := &crew.Agent{
		Name:     "demo",
		Triggers: []crew.Trigger{{Type: crew.TriggerWebhook, Route: "/a"}},
	}
	if _, err := r.Reconcile(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	originalPath := filepath.Join(home, "crew", "demo", ".reconciled.json")
	if err := os.Remove(originalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(originalPath, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(originalPath)

	agent.Triggers = append(agent.Triggers, crew.Trigger{Type: crew.TriggerWebhook, Route: "/b"})
	if _, err := r.Reconcile(context.Background(), agent); err == nil {
		t.Fatal("expected rename failure")
	}
	if _, err := os.Stat(originalPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file leaked: %v", err)
	}
	info, err := os.Stat(originalPath)
	if err != nil {
		t.Fatalf("original path missing: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("original path was clobbered: %+v", info)
	}
}

func TestWebhookReconcileNilAgent(t *testing.T) {
	t.Parallel()

	r := NewWebhookReconciler(&fakeRunner{}, t.TempDir())
	if _, err := r.Reconcile(context.Background(), nil); err == nil {
		t.Fatal("expected error")
	}
	if _, err := r.Plan(context.Background(), nil); err == nil {
		t.Fatal("expected error on Plan")
	}
	if err := r.Unreconcile(context.Background(), nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestNewWebhookReconcilerDefaultRunner(t *testing.T) {
	t.Parallel()

	r := NewWebhookReconciler(nil, t.TempDir())
	if _, ok := r.Runner.(ExecRunner); !ok {
		t.Fatalf("default runner should be ExecRunner, got %T", r.Runner)
	}
}

func TestWebhookPlanDoesNotMutate(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeRunner{}
	r := NewWebhookReconciler(runner, home)
	agent := &crew.Agent{
		Name:     "demo",
		Triggers: []crew.Trigger{{Type: crew.TriggerWebhook, Route: "/a"}},
	}
	diff, err := r.Plan(context.Background(), agent)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(diff.Added) != 1 {
		t.Fatalf("Added: %+v", diff.Added)
	}
	if got := len(runner.callsWith("fairway route add")); got != 0 {
		t.Fatalf("plan must not call route add; got %d", got)
	}
	if _, err := os.Stat(filepath.Join(home, "crew", "demo", ".reconciled.json")); !os.IsNotExist(err) {
		t.Fatalf("plan must not write state file: %v", err)
	}
}

func TestWebhookRegisteredAtPreservedAcrossReconciles(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeRunner{}
	r := NewWebhookReconciler(runner, home)

	t1 := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	r.NowFunc = func() time.Time { return t1 }
	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerWebhook, Route: "/a"},
		},
	}
	if _, err := r.Reconcile(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	r.NowFunc = func() time.Time { return t1.Add(24 * time.Hour) }
	if _, err := r.Reconcile(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "crew", "demo", ".reconciled.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc reconciledFile
	_ = json.Unmarshal(raw, &doc)
	if len(doc.Webhooks) != 1 {
		t.Fatalf("want 1 webhook, got %d", len(doc.Webhooks))
	}
	if !doc.Webhooks[0].RegisteredAt.Equal(t1) {
		t.Fatalf("registered_at changed across reconciles: got %v want %v", doc.Webhooks[0].RegisteredAt, t1)
	}
}
