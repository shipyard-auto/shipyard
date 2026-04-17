package fairway_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// ── fakeRepo ──────────────────────────────────────────────────────────────────

// fakeRepo is a test-double for Repository that holds config in memory and
// can be configured to fail on Save.
type fakeRepo struct {
	mu      sync.Mutex
	cfg     fairway.Config
	loadErr error
	saveErr error
	saved   int // count of successful Save calls
}

func (f *fakeRepo) Load() (fairway.Config, error) {
	if f.loadErr != nil {
		return fairway.Config{}, f.loadErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg, nil
}

func (f *fakeRepo) Save(cfg fairway.Config) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
	f.saved++
	return nil
}

func (f *fakeRepo) Path() string { return "/fake/routes.json" }

// slowSaveRepo wraps fakeRepo and sleeps before saving, to allow locking tests.
type slowSaveRepo struct {
	fakeRepo
	delay time.Duration
}

func (s *slowSaveRepo) Save(cfg fairway.Config) error {
	time.Sleep(s.delay)
	return s.fakeRepo.Save(cfg)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func baseConfig() fairway.Config {
	return fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          fairway.DefaultPort,
		Bind:          fairway.DefaultBind,
		Routes:        []fairway.Route{},
	}
}

func makeRoute(path string) fairway.Route {
	return fairway.Route{
		Path:   path,
		Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
		Action: fairway.Action{Type: fairway.ActionCronRun, Target: "job"},
	}
}

func newRouterWithRoutes(t *testing.T, paths ...string) *fairway.Router {
	t.Helper()
	cfg := baseConfig()
	for _, p := range paths {
		cfg.Routes = append(cfg.Routes, makeRoute(p))
	}
	repo := &fakeRepo{cfg: cfg}
	return fairway.NewRouterWithConfig(repo, cfg)
}

// ── Construction ──────────────────────────────────────────────────────────────

func TestNewRouter_loadsFromRepo(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.Routes = []fairway.Route{makeRoute("/a"), makeRoute("/b")}
	repo := &fakeRepo{cfg: cfg}

	r, err := fairway.NewRouter(repo)
	if err != nil {
		t.Fatalf("NewRouter() error: %v", err)
	}

	routes := r.List()
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
}

func TestNewRouter_propagatesLoadError(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{loadErr: fmt.Errorf("disk read failed")}
	_, err := fairway.NewRouter(repo)
	if err == nil {
		t.Fatal("NewRouter() expected error from repo.Load(), got nil")
	}
}

func TestNewRouter_rejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	bad := fairway.Config{SchemaVersion: "99", Port: 9876, Bind: "127.0.0.1"}
	repo := &fakeRepo{cfg: bad}
	_, err := fairway.NewRouter(repo)
	if err == nil {
		t.Fatal("NewRouter() expected validation error, got nil")
	}
	if !errors.Is(err, fairway.ErrUnsupportedSchema) {
		t.Errorf("expected ErrUnsupportedSchema, got %v", err)
	}
}

// ── Match ─────────────────────────────────────────────────────────────────────

func TestMatch_exactPathHit(t *testing.T) {
	t.Parallel()

	r := newRouterWithRoutes(t, "/hooks/github")
	got, ok := r.Match("/hooks/github")
	if !ok {
		t.Fatal("Match() expected hit, got miss")
	}
	if got.Path != "/hooks/github" {
		t.Errorf("Match() path = %q; want /hooks/github", got.Path)
	}
}

func TestMatch_nonexistentPath(t *testing.T) {
	t.Parallel()

	r := newRouterWithRoutes(t, "/a")
	_, ok := r.Match("/b")
	if ok {
		t.Fatal("Match() expected miss, got hit")
	}
}

func TestMatch_caseSensitive(t *testing.T) {
	t.Parallel()

	r := newRouterWithRoutes(t, "/hooks/github")
	_, ok := r.Match("/Hooks/Github")
	if ok {
		t.Fatal("Match() should be case-sensitive: /Hooks/Github must not match /hooks/github")
	}
}

func TestMatch_trailingSlashDistinct(t *testing.T) {
	t.Parallel()

	r := newRouterWithRoutes(t, "/x")
	_, ok := r.Match("/x/")
	if ok {
		t.Fatal("Match() /x/ must not match /x")
	}
}

// ── List ──────────────────────────────────────────────────────────────────────

func TestList_returnsCloneNotSliceRef(t *testing.T) {
	t.Parallel()

	r := newRouterWithRoutes(t, "/a", "/b")

	first := r.List()
	if len(first) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(first))
	}

	// Mutate the returned slice.
	first[0] = makeRoute("/mutated")

	second := r.List()
	if second[0].Path == "/mutated" {
		t.Error("List() returned a reference to the internal slice; mutation propagated")
	}
}

// ── Add ───────────────────────────────────────────────────────────────────────

func TestAdd_persistsAndUpdatesMemory(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{cfg: baseConfig()}
	r, _ := fairway.NewRouter(repo)

	if err := r.Add(makeRoute("/new")); err != nil {
		t.Fatalf("Add() error: %v", err)
	}

	if repo.saved != 1 {
		t.Errorf("expected 1 Save call, got %d", repo.saved)
	}
	_, ok := r.Match("/new")
	if !ok {
		t.Error("Match() should find /new after Add()")
	}
}

func TestAdd_duplicatePath_returnsError_noPersist(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.Routes = []fairway.Route{makeRoute("/dup")}
	repo := &fakeRepo{cfg: cfg}
	r := fairway.NewRouterWithConfig(repo, cfg)

	err := r.Add(makeRoute("/dup"))
	if err == nil {
		t.Fatal("Add() expected error for duplicate path")
	}
	if !errors.Is(err, fairway.ErrDuplicateRoutePath) {
		t.Errorf("expected ErrDuplicateRoutePath, got %v", err)
	}
	if repo.saved != 0 {
		t.Errorf("Save must not be called on duplicate, got %d calls", repo.saved)
	}
}

func TestAdd_invalidRoute_returnsError_noPersist(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{cfg: baseConfig()}
	r, _ := fairway.NewRouter(repo)

	bad := fairway.Route{Path: "no-slash", Auth: fairway.Auth{Type: fairway.AuthLocalOnly}, Action: fairway.Action{Type: fairway.ActionCronRun, Target: "j"}}
	err := r.Add(bad)
	if err == nil {
		t.Fatal("Add() expected validation error")
	}
	if repo.saved != 0 {
		t.Errorf("Save must not be called on invalid route")
	}
}

func TestAdd_repoSaveFails_memoryUnchanged(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	repo := &fakeRepo{cfg: cfg, saveErr: fmt.Errorf("disk full")}
	r := fairway.NewRouterWithConfig(repo, cfg)

	err := r.Add(makeRoute("/new"))
	if err == nil {
		t.Fatal("Add() expected error when Save fails")
	}
	if _, ok := r.Match("/new"); ok {
		t.Error("memory must not be updated when Save fails")
	}
	if len(r.List()) != 0 {
		t.Error("route list must be unchanged when Save fails")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestDelete_removesAndPersists(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.Routes = []fairway.Route{makeRoute("/del-me"), makeRoute("/keep")}
	repo := &fakeRepo{cfg: cfg}
	r := fairway.NewRouterWithConfig(repo, cfg)

	if err := r.Delete("/del-me"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}

	if repo.saved != 1 {
		t.Errorf("expected 1 Save call, got %d", repo.saved)
	}
	if _, ok := r.Match("/del-me"); ok {
		t.Error("/del-me should no longer match after Delete()")
	}
	if _, ok := r.Match("/keep"); !ok {
		t.Error("/keep should still match after Delete()")
	}
}

func TestDelete_missingPath_returnsErrRouteNotFound_noPersist(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{cfg: baseConfig()}
	r, _ := fairway.NewRouter(repo)

	err := r.Delete("/nonexistent")
	if err == nil {
		t.Fatal("Delete() expected ErrRouteNotFound")
	}
	if !errors.Is(err, fairway.ErrRouteNotFound) {
		t.Errorf("expected ErrRouteNotFound, got %v", err)
	}
	if repo.saved != 0 {
		t.Errorf("Save must not be called when route not found")
	}
}

func TestDelete_repoSaveFails_memoryUnchanged(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.Routes = []fairway.Route{makeRoute("/existing")}
	repo := &fakeRepo{cfg: cfg, saveErr: fmt.Errorf("io error")}
	r := fairway.NewRouterWithConfig(repo, cfg)

	err := r.Delete("/existing")
	if err == nil {
		t.Fatal("Delete() expected error when Save fails")
	}
	if _, ok := r.Match("/existing"); !ok {
		t.Error("memory must not be updated when Save fails")
	}
}

// ── Replace ───────────────────────────────────────────────────────────────────

func TestReplace_updatesInPlace_preservesOrder(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.Routes = []fairway.Route{makeRoute("/a"), makeRoute("/b"), makeRoute("/c")}
	repo := &fakeRepo{cfg: cfg}
	r := fairway.NewRouterWithConfig(repo, cfg)

	updated := makeRoute("/b")
	updated.Action.Target = "updated-job"

	if err := r.Replace(updated); err != nil {
		t.Fatalf("Replace() error: %v", err)
	}

	routes := r.List()
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes after Replace, got %d", len(routes))
	}
	if routes[0].Path != "/a" || routes[1].Path != "/b" || routes[2].Path != "/c" {
		t.Errorf("order changed after Replace: %v", routePaths(routes))
	}
	if routes[1].Action.Target != "updated-job" {
		t.Errorf("route /b not updated: target = %q", routes[1].Action.Target)
	}
	if repo.saved != 1 {
		t.Errorf("expected 1 Save, got %d", repo.saved)
	}
}

func TestReplace_missingPath_returnsErrRouteNotFound(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{cfg: baseConfig()}
	r, _ := fairway.NewRouter(repo)

	err := r.Replace(makeRoute("/ghost"))
	if err == nil {
		t.Fatal("Replace() expected ErrRouteNotFound")
	}
	if !errors.Is(err, fairway.ErrRouteNotFound) {
		t.Errorf("expected ErrRouteNotFound, got %v", err)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestConcurrentReadsDontBlockEachOther(t *testing.T) {
	t.Parallel()

	r := newRouterWithRoutes(t, "/a", "/b", "/c")

	const readers = 100
	var wg sync.WaitGroup
	wg.Add(readers)

	start := make(chan struct{})
	results := make([]bool, readers)

	for i := range readers {
		go func(i int) {
			defer wg.Done()
			<-start
			_, ok := r.Match("/a")
			results[i] = ok
		}(i)
	}

	close(start)
	wg.Wait()

	for i, ok := range results {
		if !ok {
			t.Errorf("reader %d: Match(/a) returned false", i)
		}
	}
}

func TestRaceFreeUnderStress(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{cfg: baseConfig()}
	r, _ := fairway.NewRouter(repo)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	deadline := time.Now().Add(500 * time.Millisecond)

	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			paths := []string{"/p1", "/p2", "/p3", "/p4", "/p5"}
			for time.Now().Before(deadline) {
				path := paths[rand.IntN(len(paths))]
				switch rand.IntN(3) {
				case 0:
					r.Match(path)
				case 1:
					_ = r.Add(makeRoute(path))
				case 2:
					_ = r.Delete(path)
				}
			}
		}(i)
	}

	wg.Wait()
	// No race detector errors = pass.
}

func TestConfig_returnsSnapshot(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.Port = 8888
	repo := &fakeRepo{cfg: cfg}
	r := fairway.NewRouterWithConfig(repo, cfg)

	snapshot := r.Config()
	if snapshot.Port != 8888 {
		t.Errorf("Config().Port = %d; want 8888", snapshot.Port)
	}
}

func TestReplace_repoSaveFails_memoryUnchanged(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.Routes = []fairway.Route{makeRoute("/existing")}
	repo := &fakeRepo{cfg: cfg, saveErr: fmt.Errorf("io error")}
	r := fairway.NewRouterWithConfig(repo, cfg)

	updated := makeRoute("/existing")
	updated.Action.Target = "new-target"

	err := r.Replace(updated)
	if err == nil {
		t.Fatal("Replace() expected error when Save fails")
	}

	// Memory should still have original target.
	got, ok := r.Match("/existing")
	if !ok {
		t.Fatal("route /existing should still be present after failed Replace")
	}
	if got.Action.Target == "new-target" {
		t.Error("memory was updated despite Save failure")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func routePaths(routes []fairway.Route) []string {
	paths := make([]string, len(routes))
	for i, r := range routes {
		paths[i] = r.Path
	}
	return paths
}
