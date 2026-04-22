package addon

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

func withDetect(t *testing.T, fn func(Kind) (Info, error)) {
	t.Helper()
	prev := detectFn
	detectFn = fn
	t.Cleanup(func() { detectFn = prev })
}

func TestRequirePreRunPassesWhenInstalled(t *testing.T) {
	withDetect(t, func(k Kind) (Info, error) {
		return Info{Kind: k, Installed: true, BinaryPath: "/fake"}, nil
	})
	pre := RequirePreRun(KindCrew)
	if err := pre(&cobra.Command{}, nil); err != nil {
		t.Fatalf("PreRun error = %v; want nil", err)
	}
}

func TestRequirePreRunFailsWhenMissing(t *testing.T) {
	withDetect(t, func(k Kind) (Info, error) {
		return Info{Kind: k, Installed: false}, nil
	})
	pre := RequirePreRun(KindFairway)
	err := pre(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("PreRun returned nil; want error")
	}
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v; want ErrNotInstalled wrapped", err)
	}
}

func TestRequirePreRunPropagatesDetectorError(t *testing.T) {
	sentinel := errors.New("boom")
	withDetect(t, func(k Kind) (Info, error) { return Info{}, sentinel })
	pre := RequirePreRun(KindCrew)
	err := pre(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v; want ErrNotInstalled wrapper", err)
	}
}
