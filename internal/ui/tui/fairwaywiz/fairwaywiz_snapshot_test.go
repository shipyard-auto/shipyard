//go:build !race

// Snapshot tests for the TUI screens. Gated behind `!race` because the
// underlying bubbletea event loop races with key input under the race
// detector — keys arrive before the initial Init/WindowSize tick has
// finished draining, so the model is in an inconsistent state. The race
// is a test-framework artifact, not a production bug.

package fairwaywiz

import (
	"bytes"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
)

func TestRoot_snapshotScreens(t *testing.T) {
	svc := &fakeClient{routes: []fairwayctl.Route{{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "AB12CD"}}}}
	tm := teatest.NewTestModel(t, NewRoot(svc), teatest.WithInitialTermSize(100, 30))
	t.Cleanup(func() { _ = tm.Quit() })

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Fairway Config"))
	})

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Routes (1)"))
	}, teatest.WithDuration(10*time.Second))
}

func TestRoot_snapshotFormScreen(t *testing.T) {
	svc := &fakeClient{}
	tm := teatest.NewTestModel(t, NewRoot(svc), teatest.WithInitialTermSize(100, 30))
	t.Cleanup(func() { _ = tm.Quit() })

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Fairway Config"))
	})

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("No routes configured yet."))
	}, teatest.WithDuration(10*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Step 1 of"))
	})
}
