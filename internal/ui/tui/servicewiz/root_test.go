package servicewiz

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	svcpkg "github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type fakeServiceAPI struct {
	records []svcpkg.ServiceRecord
	added   []svcpkg.ServiceInput
	updated []struct {
		ID    string
		Input svcpkg.ServiceInput
	}
	deleted []string
}

func (f *fakeServiceAPI) List() ([]svcpkg.ServiceRecord, error) {
	return append([]svcpkg.ServiceRecord{}, f.records...), nil
}
func (f *fakeServiceAPI) Get(id string) (svcpkg.ServiceRecord, error) {
	for _, record := range f.records {
		if record.ID == id {
			return record, nil
		}
	}
	return svcpkg.ServiceRecord{}, svcpkg.ErrServiceNotFound
}
func (f *fakeServiceAPI) Add(input svcpkg.ServiceInput) (svcpkg.ServiceRecord, error) {
	f.added = append(f.added, input)
	record := svcpkg.ServiceRecord{ID: "AB12CD", Name: *input.Name, Command: *input.Command, Enabled: *input.Enabled, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.records = append(f.records, record)
	return record, nil
}
func (f *fakeServiceAPI) Update(id string, patch svcpkg.ServiceInput) (svcpkg.ServiceRecord, error) {
	f.updated = append(f.updated, struct {
		ID    string
		Input svcpkg.ServiceInput
	}{id, patch})
	return f.Get(id)
}
func (f *fakeServiceAPI) Delete(id string) error                          { f.deleted = append(f.deleted, id); return nil }
func (f *fakeServiceAPI) Enable(id string) (svcpkg.ServiceRecord, error)  { return f.Get(id) }
func (f *fakeServiceAPI) Disable(id string) (svcpkg.ServiceRecord, error) { return f.Get(id) }
func (f *fakeServiceAPI) Start(id string) (svcpkg.ServiceRecord, error)   { return f.Get(id) }
func (f *fakeServiceAPI) Stop(id string) (svcpkg.ServiceRecord, error)    { return f.Get(id) }
func (f *fakeServiceAPI) Restart(id string) (svcpkg.ServiceRecord, error) { return f.Get(id) }
func (f *fakeServiceAPI) Status(id string) (svcpkg.ServiceRecord, svcpkg.RuntimeStatus, error) {
	record, err := f.Get(id)
	return record, svcpkg.RuntimeStatus{State: "active", PID: 42}, err
}

func TestRootMenuEmptyState(t *testing.T) {
	root := NewRoot(&fakeServiceAPI{})
	if !strings.Contains(root.View(), "No services yet") {
		t.Fatalf("expected empty state, got %q", root.View())
	}
}

func TestAddScreenHappyPath(t *testing.T) {
	svc := &fakeServiceAPI{}
	screen := newAddScreen(theme.New(), svc, nil)
	for _, r := range "Gateway" {
		screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for _, r := range "/bin/echo ok" {
		screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
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

func TestDeleteScreenDangerousConfirmDefaultsToCancel(t *testing.T) {
	svc := &fakeServiceAPI{records: []svcpkg.ServiceRecord{{ID: "AB12CD", Name: "Gateway", Command: "/bin/echo ok"}}}
	screen := newDeleteScreen(theme.New(), svc, "AB12CD")
	view := screen.View()
	if !strings.Contains(view, "Cancel") || !strings.Contains(view, "Confirm") {
		t.Fatalf("expected confirm controls, got %q", view)
	}
}

func TestRootWithTeaTestAddFlow(t *testing.T) {
	svc := &fakeServiceAPI{}
	tm := teatest.NewTestModel(t, NewRoot(svc), teatest.WithInitialTermSize(100, 30))
	t.Cleanup(func() { _ = tm.Quit() })
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("Start service")) })
}
