package fairway

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeRepository struct {
	mu sync.Mutex

	loadConfig Config
	loadErr    error

	saveErr  error
	saveHook func(Config) error

	saveCalls int
	saved     []Config
}

func (f *fakeRepository) Load() (Config, error) {
	if f.loadErr != nil {
		return Config{}, f.loadErr
	}
	return cloneConfig(f.loadConfig), nil
}

func (f *fakeRepository) Save(cfg Config) error {
	if f.saveHook != nil {
		if err := f.saveHook(cloneConfig(cfg)); err != nil {
			return err
		}
	}
	if f.saveErr != nil {
		return f.saveErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveCalls++
	f.saved = append(f.saved, cloneConfig(cfg))
	return nil
}

func (f *fakeRepository) Path() string {
	return "/tmp/routes.json"
}

func (f *fakeRepository) SaveCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saveCalls
}

func TestRouterConstruction(t *testing.T) {
	t.Run("NewRouter_loadsFromRepo", func(t *testing.T) {
		repo := &fakeRepository{
			loadConfig: Config{
				SchemaVersion: SchemaVersion,
				Port:          DefaultPort,
				Bind:          DefaultBind,
				Routes: []Route{
					testRoute("/hooks/github"),
					testRoute("/hooks/grafana"),
				},
			},
		}

		router, err := NewRouter(repo)
		if err != nil {
			t.Fatalf("NewRouter() error = %v", err)
		}

		got := router.List()
		if len(got) != 2 {
			t.Fatalf("len(List()) = %d, want 2", len(got))
		}
	})

	t.Run("NewRouter_propagatesLoadError", func(t *testing.T) {
		repo := &fakeRepository{loadErr: errors.New("boom")}
		_, err := NewRouter(repo)
		if err == nil || err.Error() != "boom" {
			t.Fatalf("NewRouter() error = %v, want load error", err)
		}
	})

	t.Run("NewRouter_rejectsInvalidConfig", func(t *testing.T) {
		repo := &fakeRepository{
			loadConfig: Config{
				SchemaVersion: "2",
				Port:          DefaultPort,
				Bind:          DefaultBind,
				Routes:        []Route{},
			},
		}

		_, err := NewRouter(repo)
		if !errors.Is(err, ErrUnsupportedSchema) {
			t.Fatalf("NewRouter() error = %v, want ErrUnsupportedSchema", err)
		}
	})
}

func TestRouterMatch(t *testing.T) {
	router := newTestRouter()

	t.Run("Match_exactPathHit", func(t *testing.T) {
		route, ok := router.Match("/hooks/github")
		if !ok {
			t.Fatal("Match() ok = false, want true")
		}
		if route.Path != "/hooks/github" {
			t.Fatalf("Match().Path = %q, want /hooks/github", route.Path)
		}
	})

	t.Run("Match_nonexistentPath", func(t *testing.T) {
		route, ok := router.Match("/missing")
		if ok {
			t.Fatalf("Match() ok = true, route = %#v, want false", route)
		}
		if !reflect.DeepEqual(route, Route{}) {
			t.Fatalf("Match() route = %#v, want zero Route", route)
		}
	})

	t.Run("Match_caseSensitive", func(t *testing.T) {
		_, ok := router.Match("/Hooks/Github")
		if ok {
			t.Fatal("Match() ok = true, want false")
		}
	})

	t.Run("Match_trailingSlashDistinct", func(t *testing.T) {
		router := NewRouterWithConfig(&fakeRepository{}, Config{
			SchemaVersion: SchemaVersion,
			Port:          DefaultPort,
			Bind:          DefaultBind,
			Routes: []Route{
				testRoute("/x"),
				testRoute("/x/"),
			},
		})

		route, ok := router.Match("/x/")
		if !ok || route.Path != "/x/" {
			t.Fatalf("Match(/x/) = (%#v, %v), want path /x/", route, ok)
		}
	})
}

func TestRouterList(t *testing.T) {
	t.Run("List_returnsCloneNotSliceRef", func(t *testing.T) {
		router := NewRouterWithConfig(&fakeRepository{}, Config{
			SchemaVersion: SchemaVersion,
			Port:          DefaultPort,
			Bind:          DefaultBind,
			Routes: []Route{
				{
					Path: "/hooks/github",
					Auth: Auth{Type: AuthBearer, Token: "secret"},
					Action: Action{
						Type:    ActionHTTPForward,
						URL:     "https://example.com/hook",
						Headers: map[string]string{"X-Test": "1"},
					},
				},
			},
		})

		routes := router.List()
		routes[0].Path = "/mutated"
		routes[0].Action.Headers["X-Test"] = "2"

		again := router.List()
		if again[0].Path != "/hooks/github" {
			t.Fatalf("List()[0].Path = %q, want /hooks/github", again[0].Path)
		}
		if again[0].Action.Headers["X-Test"] != "1" {
			t.Fatalf("List()[0].Action.Headers[X-Test] = %q, want 1", again[0].Action.Headers["X-Test"])
		}
	})
}

func TestRouterMutations(t *testing.T) {
	t.Run("Add_persistsAndUpdatesMemory", func(t *testing.T) {
		repo := &fakeRepository{loadConfig: baseConfig()}
		router, err := NewRouter(repo)
		if err != nil {
			t.Fatalf("NewRouter() error = %v", err)
		}

		route := testRoute("/hooks/new")
		if err := router.Add(route); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		if _, ok := router.Match("/hooks/new"); !ok {
			t.Fatal("Match(/hooks/new) ok = false, want true")
		}
		if repo.SaveCalls() != 1 {
			t.Fatalf("SaveCalls() = %d, want 1", repo.SaveCalls())
		}
	})

	t.Run("Add_duplicatePath_returnsError_noPersist", func(t *testing.T) {
		repo := &fakeRepository{loadConfig: baseConfig()}
		router, _ := NewRouter(repo)

		err := router.Add(testRoute("/hooks/github"))
		if !errors.Is(err, ErrDuplicateRoutePath) {
			t.Fatalf("Add() error = %v, want ErrDuplicateRoutePath", err)
		}
		if repo.SaveCalls() != 0 {
			t.Fatalf("SaveCalls() = %d, want 0", repo.SaveCalls())
		}
	})

	t.Run("Add_invalidRoute_returnsError_noPersist", func(t *testing.T) {
		repo := &fakeRepository{loadConfig: baseConfig()}
		router, _ := NewRouter(repo)

		err := router.Add(Route{Path: "invalid"})
		if !errors.Is(err, ErrInvalidRoutePath) {
			t.Fatalf("Add() error = %v, want ErrInvalidRoutePath", err)
		}
		if repo.SaveCalls() != 0 {
			t.Fatalf("SaveCalls() = %d, want 0", repo.SaveCalls())
		}
	})

	t.Run("Delete_removesAndPersists", func(t *testing.T) {
		repo := &fakeRepository{loadConfig: baseConfig()}
		router, _ := NewRouter(repo)

		if err := router.Delete("/hooks/github"); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if _, ok := router.Match("/hooks/github"); ok {
			t.Fatal("Match(/hooks/github) ok = true, want false")
		}
		if repo.SaveCalls() != 1 {
			t.Fatalf("SaveCalls() = %d, want 1", repo.SaveCalls())
		}
	})

	t.Run("Delete_missingPath_returnsErrRouteNotFound_noPersist", func(t *testing.T) {
		repo := &fakeRepository{loadConfig: baseConfig()}
		router, _ := NewRouter(repo)

		err := router.Delete("/missing")
		if !errors.Is(err, ErrRouteNotFound) {
			t.Fatalf("Delete() error = %v, want ErrRouteNotFound", err)
		}
		if repo.SaveCalls() != 0 {
			t.Fatalf("SaveCalls() = %d, want 0", repo.SaveCalls())
		}
	})

	t.Run("Replace_updatesInPlace_preservesOrder", func(t *testing.T) {
		cfg := baseConfig()
		cfg.Routes = append(cfg.Routes, testRoute("/hooks/second"))
		repo := &fakeRepository{loadConfig: cfg}
		router, _ := NewRouter(repo)

		replacement := testRoute("/hooks/github")
		replacement.Timeout = 45 * time.Second
		if err := router.Replace(replacement); err != nil {
			t.Fatalf("Replace() error = %v", err)
		}

		routes := router.List()
		if len(routes) != 2 {
			t.Fatalf("len(List()) = %d, want 2", len(routes))
		}
		if routes[0].Path != "/hooks/github" || routes[1].Path != "/hooks/second" {
			t.Fatalf("route order = %#v, want preserved order", routes)
		}
		if routes[0].Timeout != 45*time.Second {
			t.Fatalf("routes[0].Timeout = %s, want 45s", routes[0].Timeout)
		}
	})

	t.Run("Replace_missingPath_returnsErrRouteNotFound", func(t *testing.T) {
		repo := &fakeRepository{loadConfig: baseConfig()}
		router, _ := NewRouter(repo)

		err := router.Replace(testRoute("/missing"))
		if !errors.Is(err, ErrRouteNotFound) {
			t.Fatalf("Replace() error = %v, want ErrRouteNotFound", err)
		}
	})

	t.Run("Add_repoSaveFails_memoryUnchanged", func(t *testing.T) {
		repo := &fakeRepository{
			loadConfig: baseConfig(),
			saveErr:    errors.New("save failed"),
		}
		router, _ := NewRouter(repo)

		before := router.List()
		err := router.Add(testRoute("/hooks/new"))
		if err == nil || err.Error() != "save failed" {
			t.Fatalf("Add() error = %v, want save failed", err)
		}
		after := router.List()
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("router state changed on failed Add(): before=%#v after=%#v", before, after)
		}
	})

	t.Run("Delete_repoSaveFails_memoryUnchanged", func(t *testing.T) {
		repo := &fakeRepository{
			loadConfig: baseConfig(),
			saveErr:    errors.New("save failed"),
		}
		router, _ := NewRouter(repo)

		before := router.List()
		err := router.Delete("/hooks/github")
		if err == nil || err.Error() != "save failed" {
			t.Fatalf("Delete() error = %v, want save failed", err)
		}
		after := router.List()
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("router state changed on failed Delete(): before=%#v after=%#v", before, after)
		}
	})
}

func TestRouterConcurrency(t *testing.T) {
	t.Run("ConcurrentReadsDontBlockEachOther", func(t *testing.T) {
		router := newTestRouter()
		start := make(chan struct{})
		const readers = 64
		var wg sync.WaitGroup
		wg.Add(readers)

		for range readers {
			go func() {
				defer wg.Done()
				<-start
				for i := 0; i < 200; i++ {
					if _, ok := router.Match("/hooks/github"); !ok {
						t.Error("Match() ok = false, want true")
						return
					}
				}
			}()
		}

		close(start)
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent reads did not complete in time")
		}
	})

	t.Run("WriteBlocksReads", func(t *testing.T) {
		enterSave := make(chan struct{})
		releaseSave := make(chan struct{})
		repo := &fakeRepository{
			loadConfig: baseConfig(),
			saveHook: func(Config) error {
				close(enterSave)
				<-releaseSave
				return nil
			},
		}
		router, _ := NewRouter(repo)

		addDone := make(chan error, 1)
		go func() {
			addDone <- router.Add(testRoute("/hooks/new"))
		}()

		<-enterSave

		matchReady := make(chan struct{})
		matchDone := make(chan struct{})
		go func() {
			close(matchReady)
			router.Match("/hooks/github")
			close(matchDone)
		}()

		<-matchReady
		select {
		case <-matchDone:
			t.Fatal("Match() completed while write lock should still be held")
		default:
		}

		close(releaseSave)
		if err := <-addDone; err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		<-matchDone
	})

	t.Run("RaceFreeUnderStress", func(t *testing.T) {
		repo := &fakeRepository{loadConfig: baseConfig()}
		router, _ := NewRouter(repo)

		stop := make(chan struct{})
		var wg sync.WaitGroup

		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				path := fmt.Sprintf("/stress/%d", id)
				route := testRoute(path)
				for {
					select {
					case <-stop:
						return
					default:
						_, _ = router.Match("/hooks/github")
						_ = router.Add(route)
						_ = router.Delete(path)
					}
				}
			}(i)
		}

		time.AfterFunc(time.Second, func() { close(stop) })

		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("stress test did not complete")
		}
	})
}

func newTestRouter() *Router {
	return NewRouterWithConfig(&fakeRepository{}, baseConfig())
}

func baseConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Port:          DefaultPort,
		Bind:          DefaultBind,
		Routes: []Route{
			testRoute("/hooks/github"),
		},
	}
}

func testRoute(path string) Route {
	return Route{
		Path:    path,
		Timeout: DefaultActionTimeout,
		Auth:    Auth{Type: AuthBearer, Token: "secret"},
		Action:  Action{Type: ActionCronRun, Target: "job-1"},
	}
}
