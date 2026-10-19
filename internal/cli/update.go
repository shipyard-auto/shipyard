package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/addon"
	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/commsctl"
	"github.com/shipyard-auto/shipyard/internal/crewctl"
	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui"
	"github.com/shipyard-auto/shipyard/internal/update"
)

// hasInstalledAddons devolve true quando o registry de addons lista pelo
// menos um addon com Installed=true. Variável package-level para permitir
// override em testes.
var hasInstalledAddons = defaultHasInstalledAddons

// runAddonReconcileSubprocess executa o binário corrente com `update
// --skip-core`, propagando stdout/stderr para o writer do pai. Variável
// package-level para que testes substituam por uma fake — chamar exec.Cmd
// real em teste exigiria fork bomb com TestHelperProcess pattern.
var runAddonReconcileSubprocess = defaultRunAddonReconcileSubprocess

func newUpdateCmd() *cobra.Command {
	var skipCore bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update Shipyard to the latest release",
		Long: `Download the latest published Shipyard release for this platform and replace
the current binary. If shipyard-fairway, shipyard-crew or shipyard-comms is
installed, the new binary is invoked to reconcile them in the same run.`,
		Example: "shipyard update",
		RunE: func(cmd *cobra.Command, _ []string) error {
			executablePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve current executable: %w", err)
			}

			executablePath, err = filepath.EvalSymlinks(executablePath)
			if err != nil {
				executablePath = filepath.Clean(executablePath)
			}

			w := cmd.OutOrStdout()

			// --skip-core: caminho do filho re-executado pelo pai após self-
			// update. Pula o download do core (já foi feito pelo pai) e roda
			// apenas a reconciliação dos addons no binário novo.
			if skipCore {
				ui.Printf(w, "%s\n", ui.SectionTitle("Reconciling addons"))
				ui.Printf(w, "%s\n\n", ui.Muted("Running addon reconcilers from the freshly installed binary."))
				return reconcileAddons(cmd, w)
			}

			ui.Printf(w, "%s\n", ui.SectionTitle("Shipyard Update"))
			ui.Printf(w, "%s\n\n", ui.Muted("Checking the latest release and refreshing your local binary."))

			svc := update.NewService()
			result, err := svc.Run(app.Version, executablePath)
			if err != nil {
				return err
			}

			ui.Printf(w, "%s %s\n", ui.Highlight("Current:"), app.Version)
			ui.Printf(w, "%s %s\n", ui.Highlight("Latest:"), result.LatestVersion)
			ui.Printf(w, "%s %s\n\n", ui.Highlight("Target:"), result.TargetPath)

			if result.Updated {
				ui.Printf(w, "%s\n", ui.Emphasis("Shipyard updated successfully."))
				ui.Printf(w, "%s\n", ui.Muted("Run `shipyard version` to confirm the installed release."))
			} else {
				ui.Printf(w, "%s\n", ui.Emphasis("Shipyard is already up to date."))
			}

			// Quando o binário foi substituído, delegamos a reconciliação ao
			// novo binário via subprocess. Quando NÃO houve update (mesmo
			// binário nas duas pontas), reconciliamos inline — mais barato e
			// idêntico ao comportamento histórico.
			if result.Updated && hasInstalledAddons() {
				return runAddonReconcileSubprocess(cmd.Context(), executablePath, w)
			}

			return reconcileAddons(cmd, w)
		},
	}

	cmd.Flags().BoolVar(&skipCore, "skip-core", false, "Skip core update; only reconcile installed addons. Used internally by the parent process after a self-update.")

	return cmd
}

// reconcileAddons upgrades every installed addon. A fairway daemon that was
// upgraded but did not start does not stop the other addons from updating;
// its error is returned once they are done, so the update still fails loudly.
func reconcileAddons(cmd *cobra.Command, w io.Writer) error {
	fairwayErr := updateFairwayIfInstalled(cmd, w)
	if fairwayErr != nil && !errors.Is(fairwayErr, fairwayctl.ErrStartFailed) {
		return fairwayErr
	}
	if err := updateCrewIfInstalled(cmd, w); err != nil {
		return err
	}
	if err := updateCommsIfInstalled(cmd, w); err != nil {
		return err
	}
	if err := updateCommsWhatsAppIfInstalled(cmd, w); err != nil {
		return err
	}
	return fairwayErr
}

func updateFairwayIfInstalled(cmd *cobra.Command, w interface{ Write([]byte) (int, error) }) error {
	sa, err := fairwayctl.NewServiceAdder()
	if err != nil {
		return nil
	}

	installed, err := sa.IsFairwayInstalled()
	if err != nil || !installed {
		return nil
	}

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Fairway Update"))
	ui.Printf(w, "%s\n\n", ui.Muted("Checking the latest fairway release..."))

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	latestVersion, err := fairwayctl.ResolveLatestFairwayVersion(cmd.Context(), httpClient)
	if err != nil {
		ui.Printf(w, "%s %v\n", ui.Muted("Could not resolve latest fairway version:"), err)
		return nil
	}

	inst, err := buildInstaller(latestVersion)
	if err != nil {
		return fmt.Errorf("fairway: build installer: %w", err)
	}

	currentVersion, err := inst.InstalledVersion()
	if err != nil {
		currentVersion = "unknown"
	}

	ui.Printf(w, "%s %s\n", ui.Highlight("Current:"), currentVersion)
	ui.Printf(w, "%s %s\n\n", ui.Highlight("Latest:"), latestVersion)

	if err := inst.Upgrade(cmd.Context()); err != nil {
		if errors.Is(err, fairwayctl.ErrAlreadyAtVersion) {
			ui.Printf(w, "%s\n", ui.Emphasis("Fairway is already up to date."))
			return nil
		}
		if errors.Is(err, fairwayctl.ErrStartFailed) {
			_ = addon.NewRegistry("").Record(addon.KindFairway, true, inst.BinPath(), inst.Version)
			ui.Printf(w, "%s\n", ui.Emphasis("Fairway updated, but the daemon did not start."))
		}
		return err
	}
	_ = addon.NewRegistry("").Record(addon.KindFairway, true, inst.BinPath(), inst.Version)

	ui.Printf(w, "%s\n", ui.Emphasis("Fairway updated successfully."))
	return nil
}

func updateCrewIfInstalled(cmd *cobra.Command, w interface{ Write([]byte) (int, error) }) error {
	if _, err := crewctl.ResolveBinary(); err != nil {
		return nil
	}

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Crew Update"))
	ui.Printf(w, "%s\n\n", ui.Muted("Checking the latest crew release..."))

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	latestVersion, err := crewctl.ResolveLatestCrewVersion(cmd.Context(), httpClient)
	if err != nil {
		ui.Printf(w, "%s %v\n", ui.Muted("Could not resolve latest crew version:"), err)
		return nil
	}

	inst, err := buildCrewInstallerForUpdate(latestVersion)
	if err != nil {
		return fmt.Errorf("crew: build installer: %w", err)
	}

	currentVersion, err := inst.InstalledVersion()
	if err != nil {
		currentVersion = "unknown"
	}

	ui.Printf(w, "%s %s\n", ui.Highlight("Current:"), currentVersion)
	ui.Printf(w, "%s %s\n\n", ui.Highlight("Latest:"), latestVersion)

	if err := inst.Upgrade(cmd.Context()); err != nil {
		if errors.Is(err, crewctl.ErrAlreadyAtVersion) {
			ui.Printf(w, "%s\n", ui.Emphasis("Crew is already up to date."))
			return nil
		}
		return err
	}
	_ = addon.NewRegistry("").Record(addon.KindCrew, true, inst.BinPath(), inst.Version)

	ui.Printf(w, "%s\n", ui.Emphasis("Crew updated successfully."))
	printCrewRestartHint(w)
	return nil
}

// printCrewRestartHint lists the service-mode agents, which keep running the
// previous crew binary (and refuse runs with a version mismatch) until their
// OS service restarts. Restarting is left to the user: it would cut off any
// run in flight.
func printCrewRestartHint(w io.Writer) {
	agents, err := crewctl.ListAgents("")
	if err != nil {
		return
	}
	mgr, err := crewctl.NewManager()
	if err != nil {
		return
	}
	var cmds []string
	for _, a := range agents {
		if a.Mode != "service" {
			continue
		}
		if c, err := mgr.RestartCommand(a.Name); err == nil {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		return
	}
	ui.Printf(w, "%s\n", ui.Muted("Service-mode agents keep running the previous binary until restarted:"))
	for _, c := range cmds {
		ui.Printf(w, "  %s\n", c)
	}
}

// updateCommsIfInstalled is the comms-side mirror of updateCrewIfInstalled.
// Comms has no service to query (stateless v1), so installation is detected
// purely by binary presence via commsctl.ResolveBinary. The rest of the
// flow — resolve latest, build installer, call Upgrade, surface
// ErrAlreadyAtVersion as a friendly message, record the registry entry — is
// identical to the crew path.
func updateCommsIfInstalled(cmd *cobra.Command, w interface{ Write([]byte) (int, error) }) error {
	if _, err := commsctl.ResolveBinary(); err != nil {
		return nil
	}

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Comms Update"))
	ui.Printf(w, "%s\n\n", ui.Muted("Checking the latest comms release..."))

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	latestVersion, err := commsctl.ResolveLatestCommsVersion(cmd.Context(), httpClient)
	if err != nil {
		ui.Printf(w, "%s %v\n", ui.Muted("Could not resolve latest comms version:"), err)
		return nil
	}

	inst, err := buildCommsInstallerForUpdate(latestVersion)
	if err != nil {
		return fmt.Errorf("comms: build installer: %w", err)
	}

	currentVersion, err := inst.InstalledVersion()
	if err != nil {
		currentVersion = "unknown"
	}

	ui.Printf(w, "%s %s\n", ui.Highlight("Current:"), currentVersion)
	ui.Printf(w, "%s %s\n\n", ui.Highlight("Latest:"), latestVersion)

	if err := inst.Upgrade(cmd.Context()); err != nil {
		if errors.Is(err, commsctl.ErrAlreadyAtVersion) {
			ui.Printf(w, "%s\n", ui.Emphasis("Comms is already up to date."))
			return nil
		}
		return err
	}
	_ = addon.NewRegistry("").Record(addon.KindComms, true, inst.BinPath(), inst.Version)

	ui.Printf(w, "%s\n", ui.Emphasis("Comms updated successfully."))
	return nil
}

// updateCommsWhatsAppIfInstalled reconciles the WhatsApp provider binary
// like the other addons. The daemon keeps running the old binary until its
// service restarts, so the user is told to restart it.
func updateCommsWhatsAppIfInstalled(cmd *cobra.Command, w interface{ Write([]byte) (int, error) }) error {
	if _, err := commsctl.WhatsApp.ResolveBinary(); err != nil {
		return nil
	}
	ui.Printf(w, "\n%s\n", ui.SectionTitle("Comms WhatsApp Update"))
	inst, err := buildWhatsAppInstaller(cmd.Context(), "")
	if err != nil {
		ui.Printf(w, "%s %v\n", ui.Muted("Could not resolve latest comms-whatsapp version:"), err)
		return nil
	}
	current, err := inst.InstalledVersion()
	if err != nil {
		current = "unknown"
	}
	ui.Printf(w, "%s %s\n", ui.Highlight("Current:"), current)
	ui.Printf(w, "%s %s\n\n", ui.Highlight("Latest:"), inst.Version)
	if err := inst.Upgrade(cmd.Context()); err != nil {
		if errors.Is(err, commsctl.ErrAlreadyAtVersion) {
			ui.Printf(w, "%s\n", ui.Emphasis("Comms WhatsApp is already up to date."))
			return nil
		}
		return err
	}
	_ = addon.NewRegistry("").Record(addon.KindCommsWhatsApp, true, inst.BinPath(), inst.Version)
	ui.Printf(w, "%s\n", ui.Emphasis("Comms WhatsApp updated."))
	printWhatsAppRestartHint(w, listServiceRecords)
	return nil
}

// listServiceRecords is a package-level seam so tests can supply records.
var listServiceRecords = func() ([]service.ServiceRecord, error) {
	svc, err := service.NewService()
	if err != nil {
		return nil, err
	}
	return svc.List()
}

// printWhatsAppRestartHint tells the user how to restart the daemon. The
// WhatsApp daemon runs as a service the user created ('comms whatsapp serve'
// under 'shipyard service'), so the binary swap leaves the old process
// running; Shipyard does not restart a service it did not create.
func printWhatsAppRestartHint(w io.Writer, list func() ([]service.ServiceRecord, error)) {
	records, _ := list()
	var ids []string
	for _, r := range records {
		if isWhatsAppDaemonCommand(r.Command) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		ui.Printf(w, "%s\n", ui.Muted("Restart the service running 'shipyard comms whatsapp serve' so the daemon runs the new binary."))
		return
	}
	ui.Printf(w, "%s\n", ui.Muted("The daemon keeps running the previous binary until its service restarts:"))
	for _, id := range ids {
		ui.Printf(w, "  shipyard service restart %s\n", id)
	}
}

// isWhatsAppDaemonCommand matches both ways of running the daemon: through
// the core passthrough or the addon binary directly.
func isWhatsAppDaemonCommand(command string) bool {
	f := strings.Fields(command)
	for i := range f {
		if i+2 < len(f) && f[i+1] == "whatsapp" && f[i+2] == "serve" && strings.HasSuffix(f[i], "comms") {
			return true
		}
		if i+1 < len(f) && f[i+1] == "serve" && strings.HasSuffix(f[i], "shipyard-comms-whatsapp") {
			return true
		}
	}
	return false
}

// buildCommsInstallerForUpdate builds a production comms Installer for the
// update flow. Mirrors buildCrewInstallerForUpdate; lives here so the
// constructor doesn't have to leak from internal/cli/comms.go for one
// extra caller.
func buildCommsInstallerForUpdate(version string) (*commsctl.Installer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("comms: home dir: %w", err)
	}
	return &commsctl.Installer{
		Version:     version,
		Platform:    commsctl.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		BinDir:      filepath.Join(home, ".local", "bin"),
		StateDir:    filepath.Join(home, ".shipyard", "comms"),
		HTTPClient:  &http.Client{Timeout: 5 * time.Minute},
		ReleaseBase: commsctl.DefaultReleaseBase,
		Now:         time.Now,
	}, nil
}

// buildCrewInstallerForUpdate builds a production crew Installer for the
// update flow. It mirrors the builder in internal/cli/crew/install.go but
// lives here to avoid leaking an exported constructor just for one caller.
func buildCrewInstallerForUpdate(version string) (*crewctl.Installer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("crew: home dir: %w", err)
	}
	return &crewctl.Installer{
		Version:     version,
		Platform:    crewctl.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		BinDir:      filepath.Join(home, ".local", "bin"),
		StateDir:    filepath.Join(home, ".shipyard", "crew"),
		RunDir:      filepath.Join(home, ".shipyard", "run", "crew"),
		HTTPClient:  crewctl.DefaultHTTPClient(),
		ReleaseBase: crewctl.DefaultReleaseBase,
	}, nil
}

// defaultHasInstalledAddons reads the addon registry and returns true when
// any addon is marked as installed. Returns false on any registry error —
// the worst case is missing the subprocess delegation, which falls back to
// the historical inline reconciliation.
func defaultHasInstalledAddons() bool {
	reg := addon.NewRegistry("")
	file, err := reg.Load()
	if err != nil || file == nil {
		return false
	}
	for _, info := range file.Addons {
		if info != nil && info.Installed {
			return true
		}
	}
	return false
}

// defaultRunAddonReconcileSubprocess invokes the binary at binPath with
// `update --skip-core`, wiring stdout/stderr back to the parent's writer.
// The call blocks until the child exits; the child's exit code is propagated
// as the error return.
func defaultRunAddonReconcileSubprocess(ctx context.Context, binPath string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, binPath, "update", "--skip-core")
	cmd.Stdout = w
	cmd.Stderr = w
	cmd.Stdin = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("reconcile addons via new binary: %w", err)
	}
	return nil
}
