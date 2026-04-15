package components

import (
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

func TestHeaderRenderSmoke(t *testing.T) {
	h := NewHeader(theme.New(), "Cron Control Panel", []string{"cron"})
	view := h.View()
	if !strings.Contains(view, "Cron Control Panel") {
		t.Fatalf("expected title in header view: %q", view)
	}
}
