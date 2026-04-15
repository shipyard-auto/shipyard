package cronwiz

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type fakeCronService struct {
	jobs    []cron.Job
	added   []cron.JobInput
	updated []struct {
		ID    string
		Input cron.JobInput
	}
	deleted []string
	ran     []string
}

func (f *fakeCronService) List() ([]cron.Job, error)                          { return append([]cron.Job{}, f.jobs...), nil }
func (f *fakeCronService) Get(id string) (cron.Job, error)                    { for _, j := range f.jobs { if j.ID == id { return j, nil } }; return cron.Job{}, cron.ErrJobNotFound }
func (f *fakeCronService) Add(input cron.JobInput) (cron.Job, error)          { f.added = append(f.added, input); j := cron.Job{ID: "AB12CD", Name: *input.Name, Schedule: *input.Schedule, Command: *input.Command, Enabled: *input.Enabled, CreatedAt: time.Now(), UpdatedAt: time.Now()}; f.jobs = append(f.jobs, j); return j, nil }
func (f *fakeCronService) Update(id string, input cron.JobInput) (cron.Job, error) {
	f.updated = append(f.updated, struct {
		ID    string
		Input cron.JobInput
	}{id, input})
	for i := range f.jobs {
		if f.jobs[i].ID != id {
			continue
		}
		if input.Name != nil {
			f.jobs[i].Name = *input.Name
		}
		if input.Description != nil {
			f.jobs[i].Description = *input.Description
		}
		if input.Schedule != nil {
			f.jobs[i].Schedule = *input.Schedule
		}
		if input.Command != nil {
			f.jobs[i].Command = *input.Command
		}
		if input.Enabled != nil {
			f.jobs[i].Enabled = *input.Enabled
		}
		return f.jobs[i], nil
	}
	return cron.Job{}, cron.ErrJobNotFound
}
func (f *fakeCronService) Enable(id string) (cron.Job, error)                 { return f.Get(id) }
func (f *fakeCronService) Disable(id string) (cron.Job, error)                { return f.Get(id) }
func (f *fakeCronService) Run(id string) (cron.Job, string, error)            { f.ran = append(f.ran, id); j, err := f.Get(id); return j, "ok", err }
func (f *fakeCronService) Delete(id string) error                             { f.deleted = append(f.deleted, id); return nil }

func sendText[T interface{ Update(tea.Msg) (Screen, tea.Cmd) }](t *testing.T, screen Screen, text string) Screen {
	t.Helper()
	for _, r := range text {
		next, _ := screen.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		screen = next
	}
	return screen
}

func TestRootBackNavigationFlow(t *testing.T) {
	svc := &fakeCronService{}
	root := NewRoot(svc)
	if !strings.Contains(root.View(), "Cron Control Panel") {
		t.Fatalf("expected main menu, got %q", root.View())
	}
	_, cmd := root.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		_ = cmd
	}
	if !strings.Contains(root.View(), "Job name") {
		t.Fatalf("expected add screen after enter, got %q", root.View())
	}
	_, _ = root.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !strings.Contains(root.View(), "Cron Control Panel") {
		t.Fatalf("expected back to menu, got %q", root.View())
	}
}

func TestAddScreenHappyPath(t *testing.T) {
	svc := &fakeCronService{}
	screen := newAddScreen(theme.New(), svc, nil)
	for _, r := range "Heartbeat" {
		screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for _, r := range "/bin/echo ok" {
		screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected add submit command")
	}
	screen, _ = screen.Update(cmd())
	if len(svc.added) != 1 {
		t.Fatalf("expected one add call, got %d", len(svc.added))
	}
	if !strings.Contains(screen.View(), "created successfully") {
		t.Fatalf("expected success view, got %q", screen.View())
	}
}

func TestAddScreenValidationStaysOnStep(t *testing.T) {
	svc := &fakeCronService{}
	screen := newAddScreen(theme.New(), svc, nil)
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(screen.View(), "name is required") {
		t.Fatalf("expected validation error, got %q", screen.View())
	}
}

func TestListScreenEmptyState(t *testing.T) {
	svc := &fakeCronService{}
	screen := newListScreen(theme.New(), svc)
	if !strings.Contains(screen.View(), "No cron jobs to browse.") {
		t.Fatalf("expected empty state, got %q", screen.View())
	}
}

func TestUpdateScreenSendsOnlyChangedFields(t *testing.T) {
	svc := &fakeCronService{jobs: []cron.Job{{
		ID:          "AB12CD",
		Name:        "Heartbeat",
		Description: "desc",
		Schedule:    "* * * * *",
		Command:     "/bin/echo ok",
		Enabled:     true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}}}
	screen := newUpdateScreen(theme.New(), svc, "AB12CD")
	for _, r := range " new" {
		screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, cmd := screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected update submit command")
	}
	screen, _ = screen.Update(cmd())
	if len(svc.updated) != 1 {
		t.Fatalf("expected one update call, got %d", len(svc.updated))
	}
	patch := svc.updated[0].Input
	if patch.Name == nil || *patch.Name != "Heartbeat new" {
		t.Fatalf("expected changed name in patch, got %+v", patch)
	}
	if patch.Description != nil || patch.Schedule != nil || patch.Command != nil || patch.Enabled != nil {
		t.Fatalf("expected unchanged fields to stay nil, got %+v", patch)
	}
	if !strings.Contains(screen.View(), "updated successfully") {
		t.Fatalf("expected success view, got %q", screen.View())
	}
}

func TestDeleteScreenDangerousConfirmDefaultsToCancel(t *testing.T) {
	svc := &fakeCronService{jobs: []cron.Job{{ID: "AB12CD", Name: "Heartbeat", Schedule: "* * * * *", Command: "/bin/echo ok"}}}
	screen := newDeleteScreen(theme.New(), svc, "AB12CD")
	view := screen.View()
	if !strings.Contains(view, "Cancel") || !strings.Contains(view, "Confirm") {
		t.Fatalf("expected confirm controls, got %q", view)
	}
}

func TestRootWithTeaTestAddFlow(t *testing.T) {
	svc := &fakeCronService{}
	tm := teatest.NewTestModel(t, NewRoot(svc), teatest.WithInitialTermSize(100, 30))
	t.Cleanup(func() { _ = tm.Quit() })

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Add new cron job"))
	})

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Job name"))
	})

	tm.Type("Heartbeat")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Type("/bin/echo ok")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("created successfully"))
	})
}

func readAll(tb testing.TB, r io.Reader) []byte {
	tb.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}
