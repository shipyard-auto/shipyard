package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func newCommsStatusDeps(installed bool, version string, versionErr error, binPath, stateDir string) commsStatusDeps {
	return commsStatusDeps{
		binPath:          binPath,
		stateDir:         stateDir,
		isInstalled:      func() bool { return installed },
		installedVersion: func() (string, error) { return version, versionErr },
	}
}

func TestCommsStatus_NotInstalled(t *testing.T) {
	deps := newCommsStatusDeps(false, "", nil, "/fake/bin/shipyard-comms", "/fake/state")
	cmd := newCommsStatusCmdWith(deps)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "not installed") {
		t.Errorf("expected 'not installed' state, got: %s", out)
	}
}

func TestCommsStatus_InstalledAndFunctional(t *testing.T) {
	deps := newCommsStatusDeps(true, "0.1.0", nil, "/fake/bin/shipyard-comms", "/fake/state")
	cmd := newCommsStatusCmdWith(deps)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "installed") {
		t.Errorf("expected 'installed' state, got: %s", out)
	}
	if !strings.Contains(out, "0.1.0") {
		t.Errorf("expected version in output, got: %s", out)
	}
}

func TestCommsStatus_BinaryNotFunctional(t *testing.T) {
	deps := newCommsStatusDeps(true, "", errors.New("exec failed: permission denied"), "/fake/bin/shipyard-comms", "/fake/state")
	cmd := newCommsStatusCmdWith(deps)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "binary not functional") {
		t.Errorf("expected 'binary not functional' state, got: %s", out)
	}
	if !strings.Contains(out, "permission denied") {
		t.Errorf("expected error text in output, got: %s", out)
	}
}

func TestCommsStatus_JSONOutput(t *testing.T) {
	deps := newCommsStatusDeps(true, "0.1.0", nil, "/fake/bin/shipyard-comms", "/fake/state")
	cmd := newCommsStatusCmdWith(deps)
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)

	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set --json: %v", err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var report commsStatusReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("JSON output not parseable: %v\noutput=%s", err, buf.String())
	}
	if report.State != "installed" {
		t.Errorf("State = %q, want installed", report.State)
	}
	if report.Binary.Version != "0.1.0" {
		t.Errorf("Version = %q, want 0.1.0", report.Binary.Version)
	}
	if !report.Binary.Functional {
		t.Errorf("Functional must be true")
	}
}
