package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// isolateUpdateHome aponta $HOME para um diretório temporário antes do teste.
// Necessário porque `updateFairwayIfInstalled` e `updateCrewIfInstalled`
// resolvem caminhos sob ~/.shipyard e podem fazer HTTP real se detectarem
// addons instalados no HOME do dev/CI. Tmpdir limpo garante that ambos no-op.
func isolateUpdateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// stubUpdateHooks substitui hasInstalledAddons e runAddonReconcileSubprocess
// por doubles e restaura no t.Cleanup. Devolve ponteiro para o contador de
// chamadas do subprocess para asserts.
type updateHookStubs struct {
	subprocessCalls *int
}

func stubUpdateHooks(t *testing.T, hasAddons bool, subprocessErr error) updateHookStubs {
	t.Helper()
	origHas := hasInstalledAddons
	origRun := runAddonReconcileSubprocess

	var calls int
	hasInstalledAddons = func() bool { return hasAddons }
	runAddonReconcileSubprocess = func(ctx context.Context, p string, w io.Writer) error {
		calls++
		return subprocessErr
	}

	t.Cleanup(func() {
		hasInstalledAddons = origHas
		runAddonReconcileSubprocess = origRun
	})
	return updateHookStubs{subprocessCalls: &calls}
}

func newUpdateCmdForTest(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	cmd := newUpdateCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetContext(context.Background())
	return cmd, &buf
}

// TestUpdate_skipCoreBranch_runsReconcilersOnly exercita o caminho do filho.
// Não chama update.Service nem o registry; só roda os reconcilers de addon,
// que são no-op em HOME limpo (sem fairway/crew detectado).
func TestUpdate_skipCoreBranch_runsReconcilersOnly(t *testing.T) {
	isolateUpdateHome(t)
	stubUpdateHooks(t, false, nil)

	cmd, buf := newUpdateCmdForTest(t)
	cmd.SetArgs([]string{"--skip-core"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute --skip-core: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Reconciling addons") {
		t.Errorf("missing reconcile header on --skip-core branch:\n%s", out)
	}
	if strings.Contains(out, "Shipyard Update") {
		t.Errorf("--skip-core must NOT print the core-update header:\n%s", out)
	}
}

// TestUpdate_skipCoreBranch_doesNotCallSubprocess garante que o filho nunca
// re-spawna a si mesmo (recursão infinita).
func TestUpdate_skipCoreBranch_doesNotCallSubprocess(t *testing.T) {
	isolateUpdateHome(t)
	stubs := stubUpdateHooks(t, true, nil)

	cmd, _ := newUpdateCmdForTest(t)
	cmd.SetArgs([]string{"--skip-core"})
	_ = cmd.Execute()

	if *stubs.subprocessCalls != 0 {
		t.Errorf("--skip-core branch must NOT spawn the subprocess (would recurse), got %d calls", *stubs.subprocessCalls)
	}
}

// Os 3 testes seguintes validam a condição de delegação do caminho pai sem
// dirigir o update.Service real (que faria HTTP). A função-espelho
// shouldDelegateToSubprocess **deve** refletir literalmente a condição em
// RunE: `result.Updated && hasInstalledAddons()`. Se o RunE mudar essa
// condição, este helper PRECISA mudar junto — está no mesmo arquivo
// propositadamente para manter o acoplamento visível.

func shouldDelegateToSubprocess(updated bool) bool {
	return updated && hasInstalledAddons()
}

func TestShouldDelegate_updatedAndAddonsInstalled_delegates(t *testing.T) {
	stubUpdateHooks(t, true, nil)
	if !shouldDelegateToSubprocess(true) {
		t.Errorf("must delegate when core was updated AND addons are present")
	}
}

func TestShouldDelegate_updatedNoAddons_doesNotDelegate(t *testing.T) {
	stubUpdateHooks(t, false, nil)
	if shouldDelegateToSubprocess(true) {
		t.Errorf("must NOT delegate when no addons are installed")
	}
}

func TestShouldDelegate_notUpdated_doesNotDelegate(t *testing.T) {
	stubUpdateHooks(t, true, nil)
	if shouldDelegateToSubprocess(false) {
		t.Errorf("must NOT delegate when core was not updated (same binary both ends)")
	}
}
