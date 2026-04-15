package tty

import (
	"bytes"
	"testing"
)

func TestRequireTTYReturnsErrorForNonTTY(t *testing.T) {
	old := StdinFD
	t.Cleanup(func() { StdinFD = old })
	StdinFD = func() uintptr { return 0 }

	var stderr bytes.Buffer
	err := RequireTTY(nil, &stderr)
	if err == nil {
		t.Fatal("expected non-interactive error")
	}
	want := "This command requires an interactive terminal.\nUse the non-interactive commands instead, for example:\n  shipyard cron add --name ... --schedule ... --command ...\n"
	if stderr.String() != want {
		t.Fatalf("expected exact message %q, got %q", want, stderr.String())
	}
}
