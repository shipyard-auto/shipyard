package trigger

import (
	"context"
	"strings"
	"testing"
)

func TestExecRunnerSuccess(t *testing.T) {
	t.Parallel()

	out, err := ExecRunner{}.Run(context.Background(), "sh", "-c", "printf hello")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(out) != "hello" {
		t.Fatalf("got %q, want %q", string(out), "hello")
	}
}

func TestExecRunnerFailure(t *testing.T) {
	t.Parallel()

	out, err := ExecRunner{}.Run(context.Background(), "sh", "-c", ">&2 echo kaboom; exit 3")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected kaboom in error, got %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("stdout should be empty on failure, got %q", string(out))
	}
}
