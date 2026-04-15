package components

import (
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestViewerShowsContent(t *testing.T) {
	v := NewViewer(theme.New(), "hello")
	if !strings.Contains(v.View(), "hello") {
		t.Fatalf("expected content in viewer: %q", v.View())
	}
}
