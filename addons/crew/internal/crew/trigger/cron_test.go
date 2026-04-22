package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// ── fake runner ──────────────────────────────────────────────────────────────

type fakeCall struct {
	Args []string
}

type fakeRunner struct {
	mu    sync.Mutex
	calls []fakeCall
	// responses is a list of (matcher, stdout, err) applied in order of
	// insertion. The first matching response wins. If no matcher fires the
	// runner returns an empty stdout and nil err.
	responses []fakeResponse
}

type fakeResponse struct {
	Match  func(name string, args []string) bool
	Stdout []byte
	Err    error
}

func (r *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fakeCall{Args: append([]string{name}, args...)})
	for _, resp := range r.responses {
		if resp.Match == nil || resp.Match(name, args) {
			return resp.Stdout, resp.Err
		}
	}
	return nil, nil
}

func (r *fakeRunner) callsWith(sub string) []fakeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []fakeCall
	for _, c := range r.calls {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, sub) {
			out = append(out, c)
		}
	}
	return out
}

func matchArgsContain(sub ...string) func(string, []string) bool {
	return func(_ string, args []string) bool {
		joined := strings.Join(args, " ")
		for _, s := range sub {
			if !strings.Contains(joined, s) {
				return false
			}
		}
		return true
	}
}

// matchAllOfListThenAdd simulates a realistic list→add flow where the post-
// reconcile refresh-list should observe the newly added entries. The pair
// of stdouts is indexed by invocation count of `cron list`.
type stagedListRunner struct {
	mu        sync.Mutex
	calls     []fakeCall
	listCalls int
	lists     []([]CronEntry)
}

func (r *stagedListRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fakeCall{Args: append([]string{name}, args...)})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "cron list") {
		idx := r.listCalls
		if idx >= len(r.lists) {
			idx = len(r.lists) - 1
		}
		r.listCalls++
		if idx < 0 {
			return []byte("[]"), nil
		}
		b, _ := json.Marshal(r.lists[idx])
		return b, nil
	}
	return nil, nil
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestCronNameAndPrefix(t *testing.T) {
	t.Parallel()

	if got := CronNamePrefix("demo"); got != "crew:demo:" {
		t.Fatalf("CronNamePrefix: got %q, want %q", got, "crew:demo:")
	}
	if got := CronName("demo", 0); got != "crew:demo:0" {
		t.Fatalf("CronName: got %q, want %q", got, "crew:demo:0")
	}
	if got := CronName("job-hunter", 3); got != "crew:job-hunter:3" {
		t.Fatalf("CronName: got %q, want %q", got, "crew:job-hunter:3")
	}
}

func TestCronReconcileAddsAndRemovesStale(t *testing.T) {
	t.Parallel()

	stale := CronEntry{ID: "ABC123", Name: "crew:demo:7", Schedule: "0 0 * * *", Command: "shipyard crew run demo"}
	runner := &stagedListRunner{
		lists: [][]CronEntry{
			{stale},
			// After add, the freshly added entry shows up with a new random id.
			{stale, {ID: "XYZ789", Name: "crew:demo:0", Schedule: "*/5 * * * *", Command: "shipyard crew run demo"}},
		},
	}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerCron, Schedule: "*/5 * * * *"},
			{Type: crew.TriggerWebhook, Route: "/x"},
		},
	}
	diff, err := r.Reconcile(context.Background(), agent)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if len(diff.Added) != 1 || diff.Added[0].Name != "crew:demo:0" {
		t.Fatalf("Added unexpected: %+v", diff.Added)
	}
	if diff.Added[0].ID != "XYZ789" {
		t.Fatalf("post-apply id not backfilled: %+v", diff.Added[0])
	}
	if len(diff.Removed) != 1 || diff.Removed[0].ID != "ABC123" {
		t.Fatalf("Removed unexpected: %+v", diff.Removed)
	}
	if len(diff.Unchanged) != 0 {
		t.Fatalf("Unchanged unexpected: %+v", diff.Unchanged)
	}

	adds := runnerArgs(runner.calls, "cron add")
	if len(adds) != 1 {
		t.Fatalf("want 1 add, got %d", len(adds))
	}
	joined := strings.Join(adds[0].Args, " ")
	for _, want := range []string{
		"--name crew:demo:0",
		"--schedule */5 * * * *",
		"--command shipyard crew run demo",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("add missing %q: %s", want, joined)
		}
	}

	deletes := runnerArgs(runner.calls, "cron delete")
	if len(deletes) != 1 {
		t.Fatalf("want 1 delete, got %d", len(deletes))
	}
	if !strings.Contains(strings.Join(deletes[0].Args, " "), "ABC123") {
		t.Fatalf("delete did not target stale id: %v", deletes[0])
	}
}

func TestCronReconcileMultipleTriggersUsesIndexedNames(t *testing.T) {
	t.Parallel()

	runner := &stagedListRunner{lists: [][]CronEntry{nil, nil}}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerCron, Schedule: "* * * * *"},
			{Type: crew.TriggerCron, Schedule: "0 * * * *"},
			{Type: crew.TriggerCron, Schedule: "@daily"},
		},
	}
	if _, err := r.Reconcile(context.Background(), agent); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	adds := runnerArgs(runner.calls, "cron add")
	if len(adds) != 3 {
		t.Fatalf("want 3 adds, got %d", len(adds))
	}
	// Verify all three distinct crew:demo:<idx> names appear.
	names := map[string]bool{}
	for _, c := range adds {
		joined := strings.Join(c.Args, " ")
		for _, n := range []string{"crew:demo:0", "crew:demo:1", "crew:demo:2"} {
			if strings.Contains(joined, "--name "+n) {
				names[n] = true
			}
		}
	}
	if len(names) != 3 {
		t.Fatalf("missing indexed names; got %v", names)
	}
}

func TestCronReconcilePreservesTriggerArrayIndex(t *testing.T) {
	t.Parallel()

	runner := &stagedListRunner{lists: [][]CronEntry{nil, nil}}
	r := NewCronReconciler(runner)
	// Mix webhook and cron; the cron must be named crew:demo:1 (index 1 in
	// the full triggers array).
	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerWebhook, Route: "/wh"},
			{Type: crew.TriggerCron, Schedule: "* * * * *"},
		},
	}
	if _, err := r.Reconcile(context.Background(), agent); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	adds := runnerArgs(runner.calls, "cron add")
	if len(adds) != 1 {
		t.Fatalf("want 1 add, got %d", len(adds))
	}
	if !strings.Contains(strings.Join(adds[0].Args, " "), "--name crew:demo:1") {
		t.Fatalf("expected --name crew:demo:1, got: %v", adds[0].Args)
	}
}

func TestCronReconcileIdempotent(t *testing.T) {
	t.Parallel()

	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerCron, Schedule: "*/5 * * * *"},
		},
	}
	existing := []CronEntry{
		{ID: "ABC123", Name: "crew:demo:0", Schedule: "*/5 * * * *", Command: "shipyard crew run demo"},
	}
	runner := &stagedListRunner{lists: [][]CronEntry{existing, existing}}
	r := NewCronReconciler(runner)

	diff1, err := r.Reconcile(context.Background(), agent)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	diff2, err := r.Reconcile(context.Background(), agent)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	for i, d := range []CronDiff{diff1, diff2} {
		if len(d.Added) != 0 || len(d.Removed) != 0 {
			t.Fatalf("run %d: expected no changes, got %+v", i, d)
		}
		if len(d.Unchanged) != 1 || d.Unchanged[0].Name != "crew:demo:0" {
			t.Fatalf("run %d: unchanged %+v", i, d.Unchanged)
		}
	}
	if got := len(runnerArgs(runner.calls, "cron add")); got != 0 {
		t.Fatalf("expected no cron adds, got %d", got)
	}
	if got := len(runnerArgs(runner.calls, "cron delete")); got != 0 {
		t.Fatalf("expected no cron deletes, got %d", got)
	}
}

func TestCronReconcileScheduleChangeIsRemoveThenAdd(t *testing.T) {
	t.Parallel()

	// Same Name (crew:demo:0) but schedule changed → expect Remove+Add.
	before := []CronEntry{{ID: "OLDID1", Name: "crew:demo:0", Schedule: "0 9 * * *", Command: "shipyard crew run demo"}}
	after := []CronEntry{{ID: "NEWID1", Name: "crew:demo:0", Schedule: "0 10 * * *", Command: "shipyard crew run demo"}}
	runner := &stagedListRunner{lists: [][]CronEntry{before, after}}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{
		Name:     "demo",
		Triggers: []crew.Trigger{{Type: crew.TriggerCron, Schedule: "0 10 * * *"}},
	}
	diff, err := r.Reconcile(context.Background(), agent)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(diff.Added) != 1 || diff.Added[0].Schedule != "0 10 * * *" || diff.Added[0].ID != "NEWID1" {
		t.Fatalf("Added unexpected: %+v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].ID != "OLDID1" || diff.Removed[0].Schedule != "0 9 * * *" {
		t.Fatalf("Removed unexpected: %+v", diff.Removed)
	}
}

func TestCronReconcileNoCronTriggersUnreconcilesAll(t *testing.T) {
	t.Parallel()

	// No cron triggers in agent, but some exist in shipyard cron. All must
	// be removed.
	existing := []CronEntry{
		{ID: "OLD1", Name: "crew:demo:0", Schedule: "a"},
		{ID: "OLD2", Name: "crew:demo:1", Schedule: "b"},
	}
	runner := &stagedListRunner{lists: [][]CronEntry{existing}}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{Name: "demo"}
	diff, err := r.Reconcile(context.Background(), agent)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(diff.Added) != 0 {
		t.Fatalf("Added unexpected: %+v", diff.Added)
	}
	if len(diff.Removed) != 2 {
		t.Fatalf("want 2 removed, got %+v", diff.Removed)
	}
}

func TestCronReconcileEmptyScheduleRejected(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{
		Name: "demo",
		Triggers: []crew.Trigger{
			{Type: crew.TriggerCron, Schedule: "   "},
		},
	}
	_, err := r.Reconcile(context.Background(), agent)
	if err == nil || !strings.Contains(err.Error(), "empty schedule") {
		t.Fatalf("expected empty-schedule error, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner must not be called on validation failure; got %v", runner.calls)
	}
}

func TestCronReconcileAddError(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		responses: []fakeResponse{
			{Match: matchArgsContain("cron", "list")},
			{Match: matchArgsContain("cron", "add"), Err: errors.New("simulated failure")},
		},
	}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{
		Name:     "demo",
		Triggers: []crew.Trigger{{Type: crew.TriggerCron, Schedule: "* * * * *"}},
	}
	if _, err := r.Reconcile(context.Background(), agent); err == nil {
		t.Fatal("expected error")
	}
}

func TestCronReconcileListError(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		responses: []fakeResponse{
			{Match: matchArgsContain("cron", "list"), Err: errors.New("boom")},
		},
	}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{Name: "demo"}
	_, err := r.Reconcile(context.Background(), agent)
	if err == nil || !strings.Contains(err.Error(), "cron list") {
		t.Fatalf("expected cron list error, got %v", err)
	}
}

func TestCronReconcileListReturnsInvalidJSON(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		responses: []fakeResponse{
			{Match: matchArgsContain("cron", "list"), Stdout: []byte("not json")},
		},
	}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{Name: "demo"}
	_, err := r.Reconcile(context.Background(), agent)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestCronReconcileFilterByNamePrefixIgnoresOthers(t *testing.T) {
	t.Parallel()

	// `shipyard cron list --json` returns entries from many sources. We
	// must ignore anything not prefixed with "crew:<name>:".
	entries := []CronEntry{
		{ID: "U1", Name: "backup", Schedule: "0 * * * *"},
		{ID: "U2", Name: "crew:other:0", Schedule: "0 9 * * *"},
		{ID: "D1", Name: "crew:demo:0", Schedule: "0 9 * * *", Command: "shipyard crew run demo"},
	}
	runner := &stagedListRunner{lists: [][]CronEntry{entries, entries}}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{
		Name:     "demo",
		Triggers: []crew.Trigger{{Type: crew.TriggerCron, Schedule: "0 9 * * *"}},
	}
	diff, err := r.Reconcile(context.Background(), agent)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(diff.Removed) != 0 {
		t.Fatalf("unrelated crons must not be removed; got %+v", diff.Removed)
	}
	if len(diff.Unchanged) != 1 || diff.Unchanged[0].Name != "crew:demo:0" {
		t.Fatalf("Unchanged unexpected: %+v", diff.Unchanged)
	}
}

func TestCronPlanDoesNotMutate(t *testing.T) {
	t.Parallel()

	runner := &stagedListRunner{lists: [][]CronEntry{nil}}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{
		Name:     "demo",
		Triggers: []crew.Trigger{{Type: crew.TriggerCron, Schedule: "* * * * *"}},
	}
	diff, err := r.Plan(context.Background(), agent)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(diff.Added) != 1 {
		t.Fatalf("Added: %+v", diff.Added)
	}
	if got := len(runnerArgs(runner.calls, "cron add")); got != 0 {
		t.Fatalf("plan must not call cron add; got %d", got)
	}
	if got := len(runnerArgs(runner.calls, "cron delete")); got != 0 {
		t.Fatalf("plan must not call cron delete; got %d", got)
	}
}

func TestCronUnreconcileRemovesAll(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		responses: []fakeResponse{
			{
				Match: matchArgsContain("cron", "list"),
				Stdout: mustMarshal(t, []CronEntry{
					{ID: "AA1", Name: "crew:demo:0", Schedule: "a"},
					{ID: "BB2", Name: "crew:demo:1", Schedule: "b"},
				}),
			},
		},
	}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{Name: "demo"}
	if err := r.Unreconcile(context.Background(), agent); err != nil {
		t.Fatalf("unreconcile: %v", err)
	}
	if got := len(runner.callsWith("cron delete")); got != 2 {
		t.Fatalf("want 2 deletes, got %d", got)
	}
}

func TestCronUnreconcileNotFoundIsSilent(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		responses: []fakeResponse{
			{
				Match: matchArgsContain("cron", "list"),
				Stdout: mustMarshal(t, []CronEntry{
					{ID: "AA1", Name: "crew:demo:0", Schedule: "a"},
				}),
			},
			{Match: matchArgsContain("cron", "delete"), Err: errors.New("Shipyard cron job was not found")},
		},
	}
	r := NewCronReconciler(runner)
	agent := &crew.Agent{Name: "demo"}
	if err := r.Unreconcile(context.Background(), agent); err != nil {
		t.Fatalf("unreconcile: %v", err)
	}
}

func TestCronReconcileNilAgent(t *testing.T) {
	t.Parallel()

	r := NewCronReconciler(&fakeRunner{})
	if _, err := r.Reconcile(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil agent")
	}
	if _, err := r.Plan(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil agent on Plan")
	}
	if err := r.Unreconcile(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil agent")
	}
}

func TestNewCronReconcilerDefaultsRunner(t *testing.T) {
	t.Parallel()

	r := NewCronReconciler(nil)
	if _, ok := r.Runner.(ExecRunner); !ok {
		t.Fatalf("default runner should be ExecRunner, got %T", r.Runner)
	}
}

func TestCronDiffDeterministicOrder(t *testing.T) {
	t.Parallel()

	// Unsorted desired/current must produce alphabetically sorted output.
	desired := map[string]CronChange{
		"crew:demo:2": {Name: "crew:demo:2", Schedule: "c"},
		"crew:demo:0": {Name: "crew:demo:0", Schedule: "a"},
	}
	current := []CronEntry{
		{ID: "Z", Name: "crew:demo:9", Schedule: "z"},
		{ID: "Y", Name: "crew:demo:1", Schedule: "b"},
	}
	diff := diffCron(desired, current)
	if len(diff.Added) != 2 || diff.Added[0].Name > diff.Added[1].Name {
		t.Fatalf("Added not sorted: %+v", diff.Added)
	}
	if len(diff.Removed) != 2 || diff.Removed[0].Name > diff.Removed[1].Name {
		t.Fatalf("Removed not sorted: %+v", diff.Removed)
	}
}

// runnerArgs returns calls whose joined args contain sub.
func runnerArgs(calls []fakeCall, sub string) []fakeCall {
	var out []fakeCall
	for _, c := range calls {
		if strings.Contains(strings.Join(c.Args, " "), sub) {
			out = append(out, c)
		}
	}
	return out
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
