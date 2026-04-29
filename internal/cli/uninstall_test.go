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

// stubAddonHooks substitui as três variáveis package-level por doubles e
// restaura no t.Cleanup. Devolve ponteiros para contadores das duas
// chamadas de uninstall, para asserts no teste chamador.
type addonHookStubs struct {
	crewCalls    *int
	fairwayCalls *int
}

func stubAddonHooks(t *testing.T, kinds []addon.Kind, crewErr, fairwayErr error) addonHookStubs {
	t.Helper()
	origLoad := loadInstalledAddons
	origCrew := uninstallCrewAddon
	origFairway := uninstallFairwayAddon

	var crewCalls, fairwayCalls int
	loadInstalledAddons = func() []addon.Kind { return kinds }
	uninstallCrewAddon = func(ctx context.Context) error {
		crewCalls++
		return crewErr
	}
	uninstallFairwayAddon = func(ctx context.Context) error {
		fairwayCalls++
		return fairwayErr
	}

	t.Cleanup(func() {
		loadInstalledAddons = origLoad
		uninstallCrewAddon = origCrew
		uninstallFairwayAddon = origFairway
	})
	return addonHookStubs{crewCalls: &crewCalls, fairwayCalls: &fairwayCalls}
}

func newCmdForTest() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())
	return cmd, &buf
}

func TestCascadeUninstallAddons_runsBoth(t *testing.T) {
	isolateHome(t)
	stubs := stubAddonHooks(t,
		[]addon.Kind{addon.KindCrew, addon.KindFairway},
		nil, nil,
	)

	cmd, buf := newCmdForTest()
	cascadeUninstallAddons(cmd)

	if *stubs.crewCalls != 1 {
		t.Fatalf("crew uninstall calls: got %d want 1", *stubs.crewCalls)
	}
	if *stubs.fairwayCalls != 1 {
		t.Fatalf("fairway uninstall calls: got %d want 1", *stubs.fairwayCalls)
	}
	out := buf.String()
	if !strings.Contains(out, "Removed addon: crew") {
		t.Errorf("missing crew removal line:\n%s", out)
	}
	if !strings.Contains(out, "Removed addon: fairway") {
		t.Errorf("missing fairway removal line:\n%s", out)
	}
}

func TestCascadeUninstallAddons_continuesOnFailure(t *testing.T) {
	isolateHome(t)
	stubs := stubAddonHooks(t,
		[]addon.Kind{addon.KindCrew, addon.KindFairway},
		errors.New("simulated crew failure"), nil,
	)

	cmd, buf := newCmdForTest()
	cascadeUninstallAddons(cmd)

	if *stubs.fairwayCalls != 1 {
		t.Fatalf("fairway uninstall must run even after crew fails: got %d", *stubs.fairwayCalls)
	}
	out := buf.String()
	if !strings.Contains(out, "Warning: failed to uninstall crew") {
		t.Errorf("missing crew warning:\n%s", out)
	}
	if !strings.Contains(out, "Removed addon: fairway") {
		t.Errorf("fairway should still be reported as removed:\n%s", out)
	}
}

func TestCascadeUninstallAddons_emptyRegistryNoop(t *testing.T) {
	isolateHome(t)
	stubAddonHooks(t, nil, nil, nil)

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
		nil, nil,
	)

	cmd, buf := newCmdForTest()
	cascadeUninstallAddons(cmd)

	if *stubs.crewCalls != 1 {
		t.Fatalf("crew must still run after unknown kind: got %d", *stubs.crewCalls)
	}
	out := buf.String()
	if !strings.Contains(out, "Skipped unknown addon: ghost") {
		t.Errorf("missing skip line:\n%s", out)
	}
}
