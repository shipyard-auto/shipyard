package logwiz

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type fakeLogsService struct {
	cfg     logs.Config
	sources []logs.SourceSummary
	events  []logs.Event
	prune   logs.PruneResult
}

func (f *fakeLogsService) LoadConfig() (logs.Config, error)               { if f.cfg.RetentionDays == 0 { f.cfg.RetentionDays = 14 }; return f.cfg, nil }
func (f *fakeLogsService) SetRetentionDays(days int) (logs.Config, error) { f.cfg.RetentionDays = days; return f.cfg, nil }
func (f *fakeLogsService) ListSources() ([]logs.SourceSummary, error)     { return append([]logs.SourceSummary{}, f.sources...), nil }
func (f *fakeLogsService) Query(query logs.Query) ([]logs.Event, error)   { return append([]logs.Event{}, f.events...), nil }
func (f *fakeLogsService) Prune() (logs.PruneResult, error)               { return f.prune, nil }

func TestLogsRootMenuEmptyState(t *testing.T) {
	root := NewRoot(&fakeLogsService{})
	if !strings.Contains(root.View(), "No logs yet.") {
		t.Fatalf("expected empty state, got %q", root.View())
	}
}

func TestRetentionScreenUpdatesConfig(t *testing.T) {
	svc := &fakeLogsService{cfg: logs.Config{RetentionDays: 14}}
	screen := newRetentionScreen(theme.New(), svc)
	for range "14" {
		screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "30" {
		screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if svc.cfg.RetentionDays != 30 {
		t.Fatalf("expected retention update, got %d", svc.cfg.RetentionDays)
	}
}

func TestShowScreenEmptyResults(t *testing.T) {
	svc := &fakeLogsService{}
	screen := newShowScreen(theme.New(), svc, "")
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(screen.View(), "No log events match these filters.") {
		t.Fatalf("expected no-results empty state, got %q", screen.View())
	}
}

func TestTailScreenShowsIdleState(t *testing.T) {
	svc := &fakeLogsService{events: []logs.Event{{Timestamp: time.Now(), Source: "cron", EntityID: "AB12CD", Level: "info", Message: "hello"}}}
	screen := newTailScreen(theme.New(), svc)
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	screen, _ = screen.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(screen.View(), "Waiting for events") {
		t.Fatalf("expected idle state before tick, got %q", screen.View())
	}
}
