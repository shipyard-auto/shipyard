package theme

import (
	"os"
	"strings"
	"testing"
)

func TestThemeStylesExist(t *testing.T) {
	th := New()
	if th.TitleStyle.String() == "" {
		t.Fatal("expected title style to be initialized")
	}
	if th.PanelStyle.String() == "" {
		t.Fatal("expected panel style to be initialized")
	}
	if got := th.RenderKeyHint("q", "quit"); got == "" {
		t.Fatal("expected key hint rendering")
	}
}

func TestNoColorDisablesANSI(t *testing.T) {
	old := os.Getenv("NO_COLOR")
	t.Cleanup(func() {
		if old == "" {
			_ = os.Unsetenv("NO_COLOR")
			return
		}
		_ = os.Setenv("NO_COLOR", old)
	})

	_ = os.Setenv("NO_COLOR", "1")
	th := New()
	rendered := th.TitleStyle.Render("Shipyard")
	if strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected no ANSI when NO_COLOR is set, got %q", rendered)
	}
}
