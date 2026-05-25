package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/addon"
)

// isolateHome aponta $HOME para um diretório temporário antes do teste,
// garantindo que addon.NewRegistry("").Forget(...) escreva no tmpdir e não
// no ~/.shipyard real do desenvolvedor. Sempre chame no início do teste.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// stubAddonHooks substitui as variáveis package-level por doubles e
// restaura no t.Cleanup. Devolve ponteiros para contadores das três
// chamadas de uninstall, para asserts no teste chamador.
type addonHookStubs struct {
	crewCalls    *int
	fairwayCalls *int
	commsCalls   *int
}

func stubAddonHooks(t *testing.T, kinds []addon.Kind, crewErr, fairwayErr, commsErr error) addonHookStubs {
	t.Helper()
	origLoad := loadInstalledAddons
	origCrew := uninstallCrewAddon
	origFairway := uninstallFairwayAddon
	origComms := uninstallCommsAddon

	var crewCalls, fairwayCalls, commsCalls int
	loadInstalledAddons = func() []addon.Kind { return kinds }
	uninstallCrewAddon = func(ctx context.Context) error {
		crewCalls++
		return crewErr
	}
	uninstallFairwayAddon = func(ctx context.Context) error {
		fairwayCalls++
		return fairwayErr
	}
	uninstallCommsAddon = func(ctx context.Context) error {
		commsCalls++
		return commsErr
	}

	t.Cleanup(func() {
		loadInstalledAddons = origLoad
		uninstallCrewAddon = origCrew
		uninstallFairwayAddon = origFairway
		uninstallCommsAddon = origComms
	})
	return addonHookStubs{
		crewCalls:    &crewCalls,
		fairwayCalls: &fairwayCalls,
		commsCalls:   &commsCalls,
	}
}

func newCmdForTest() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())
	return cmd, &buf
}

func TestCascadeUninstallAddons_runsAllThree(t *testing.T) {
	isolateHome(t)
	stubs := stubAddonHooks(t,
		[]addon.Kind{addon.KindCrew, addon.KindFairway, addon.KindComms},
		nil, nil, nil,
	)

	cmd, buf := newCmdForTest()
	cascadeUninstallAddons(cmd)

	if *stubs.crewCalls != 1 {
		t.Errorf("crew uninstall calls: got %d want 1", *stubs.crewCalls)
	}
	if *stubs.fairwayCalls != 1 {
		t.Errorf("fairway uninstall calls: got %d want 1", *stubs.fairwayCalls)
	}
	if *stubs.commsCalls != 1 {
		t.Errorf("comms uninstall calls: got %d want 1", *stubs.commsCalls)
	}
	out := buf.String()
	for _, line := range []string{"Removed addon: crew", "Removed addon: fairway", "Removed addon: comms"} {
		if !strings.Contains(out, line) {
			t.Errorf("missing %q in output:\n%s", line, out)
		}
	}
}

func TestCascadeUninstallAddons_continuesOnFailure(t *testing.T) {
	isolateHome(t)
	stubs := stubAddonHooks(t,
		[]addon.Kind{addon.KindCrew, addon.KindFairway, addon.KindComms},
		errors.New("simulated crew failure"), nil, nil,
	)

	cmd, buf := newCmdForTest()
	cascadeUninstallAddons(cmd)

	if *stubs.fairwayCalls != 1 {
		t.Errorf("fairway uninstall must run even after crew fails: got %d", *stubs.fairwayCalls)
	}
	if *stubs.commsCalls != 1 {
		t.Errorf("comms uninstall must run even after crew fails: got %d", *stubs.commsCalls)
	}
	out := buf.String()
	if !strings.Contains(out, "Warning: failed to uninstall crew") {
		t.Errorf("missing crew warning:\n%s", out)
	}
	if !strings.Contains(out, "Removed addon: fairway") {
		t.Errorf("fairway should still be reported as removed:\n%s", out)
	}
	if !strings.Contains(out, "Removed addon: comms") {
		t.Errorf("comms should still be reported as removed:\n%s", out)
	}
}

func TestCascadeUninstallAddons_commsFailureDoesNotBlockOthers(t *testing.T) {
	isolateHome(t)
	stubs := stubAddonHooks(t,
		[]addon.Kind{addon.KindCrew, addon.KindFairway, addon.KindComms},
		nil, nil, errors.New("simulated comms failure"),
	)

	cmd, buf := newCmdForTest()
	cascadeUninstallAddons(cmd)

	if *stubs.crewCalls != 1 {
		t.Errorf("crew uninstall must run: got %d", *stubs.crewCalls)
	}
	if *stubs.fairwayCalls != 1 {
		t.Errorf("fairway uninstall must run: got %d", *stubs.fairwayCalls)
	}
	out := buf.String()
	if !strings.Contains(out, "Warning: failed to uninstall comms") {
		t.Errorf("missing comms warning:\n%s", out)
	}
}

func TestCascadeUninstallAddons_emptyRegistryNoop(t *testing.T) {
	isolateHome(t)
	stubAddonHooks(t, nil, nil, nil, nil)

	cmd, buf := newCmdForTest()
	cascadeUninstallAddons(cmd)

	if buf.Len() != 0 {
		t.Errorf("empty registry must produce no output, got: %q", buf.String())
	}
}

func TestCascadeUninstallAddons_skipsUnknownKind(t *testing.T) {
	isolateHome(t)
	stubs := stubAddonHooks(t,
		[]addon.Kind{addon.Kind("ghost"), addon.KindCrew},
		nil, nil, nil,
	)

	cmd, buf := newCmdForTest()
	cascadeUninstallAddons(cmd)

	if *stubs.crewCalls != 1 {
		t.Errorf("crew must still run after unknown kind: got %d", *stubs.crewCalls)
	}
	out := buf.String()
	if !strings.Contains(out, "Skipped unknown addon: ghost") {
		t.Errorf("missing skip line:\n%s", out)
	}
}

// TestCascadeUninstallAddons_commsAlone exercises the new wiring in
// isolation — confirms that a registry containing only KindComms still
// routes to uninstallCommsAddon (and not the default switch path).
func TestCascadeUninstallAddons_commsAlone(t *testing.T) {
	isolateHome(t)
	stubs := stubAddonHooks(t,
		[]addon.Kind{addon.KindComms},
		nil, nil, nil,
	)

	cmd, buf := newCmdForTest()
	cascadeUninstallAddons(cmd)

	if *stubs.commsCalls != 1 {
		t.Errorf("comms uninstall must be called exactly once: got %d", *stubs.commsCalls)
	}
	if *stubs.crewCalls != 0 || *stubs.fairwayCalls != 0 {
		t.Errorf("only comms should run, got crew=%d fairway=%d", *stubs.crewCalls, *stubs.fairwayCalls)
	}
	if !strings.Contains(buf.String(), "Removed addon: comms") {
		t.Errorf("missing comms removal line:\n%s", buf.String())
	}
}
