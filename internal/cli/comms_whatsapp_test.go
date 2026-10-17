package cli

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/internal/commsctl"
)

func TestCommsWhatsApp_PassthroughSetsProgAndForwardsArgs(t *testing.T) {
	var stdout bytes.Buffer
	deps := echoCmdHelper(t, commsPassthroughDeps{Stdout: &stdout, Stderr: &bytes.Buffer{}}, "echo-env")
	cmd := newCommsWhatsAppCmdWith(deps, nil)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"send", "Vini Eduqo", "--text", "oi", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "shipyard comms whatsapp|send,Vini Eduqo,--text,oi,--dry-run" {
		t.Fatalf("addon saw %q", got)
	}
}

func TestCommsWhatsApp_NoArgsShowsAddonHelp(t *testing.T) {
	var stdout bytes.Buffer
	deps := echoCmdHelper(t, commsPassthroughDeps{Stdout: &stdout, Stderr: &bytes.Buffer{}}, "echo-env")
	cmd := newCommsWhatsAppCmdWith(deps, nil)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stdout.String(), "|help") {
		t.Fatalf("addon saw %q", stdout.String())
	}
}

func TestCommsWhatsApp_KeepsAddonExitCode(t *testing.T) {
	deps := echoCmdHelper(t, commsPassthroughDeps{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, "refuse")
	cmd := newCommsWhatsAppCmdWith(deps, nil)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"send", "x", "--text", "y"})
	err := cmd.Execute()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("err=%v; want the addon's exit code 3 preserved", err)
	}
}

func TestCommsWhatsApp_MissingBinaryHint(t *testing.T) {
	deps := commsPassthroughDeps{
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
		ResolveBinary: func() (string, error) { return "", commsctl.ErrNotInstalled },
	}
	cmd := newCommsWhatsAppCmdWith(deps, nil)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"status"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "shipyard-comms-whatsapp is not installed") ||
		!strings.Contains(err.Error(), "shipyard comms whatsapp install") {
		t.Fatalf("err=%v", err)
	}
}

func TestCommsWhatsApp_InstallUninstallAreHandledByCore(t *testing.T) {
	inst, binDir := newCommsWhatsAppTestInstaller(t)
	var out bytes.Buffer
	cmd := newCommsWhatsAppCmdWith(commsPassthroughDeps{
		ResolveBinary: func() (string, error) { t.Fatal("install must not pass through"); return "", nil },
	}, inst)
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"uninstall"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !strings.Contains(out.String(), "shipyard-comms-whatsapp removed") || !strings.Contains(out.String(), "kept in") {
		t.Fatalf("output=%q", out.String())
	}
	_ = binDir
}

func newCommsWhatsAppTestInstaller(t *testing.T) (*commsctl.Installer, string) {
	t.Helper()
	isolateHome(t)
	bin := t.TempDir()
	return &commsctl.Installer{
		Version:  "0.3.0",
		BinDir:   bin,
		StateDir: t.TempDir(),
		Product:  commsctl.WhatsApp,
	}, bin
}
