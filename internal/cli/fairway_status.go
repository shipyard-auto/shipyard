package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui"
)

const fairwayServiceName = "fairway"

var errFairwayVersionMismatch = errors.New("fairway: version mismatch")

type fairwayStatusService interface {
	List() ([]service.ServiceRecord, error)
	Status(id string) (service.ServiceRecord, service.RuntimeStatus, error)
}

type fairwayStatusClient interface {
	Close() error
	RouteList(ctx context.Context) ([]fairwayctl.Route, error)
	Status(ctx context.Context) (fairwayctl.StatusInfo, error)
	Stats(ctx context.Context) (fairwayctl.StatsSnapshot, error)
}

type fairwayDialFunc func(context.Context, fairwayctl.Opts) (fairwayStatusClient, error)

type fairwayStatusDeps struct {
	binPath          string
	socketPath       string
	version          string
	installedVersion func() (string, error)
	newService       func() (fairwayStatusService, error)
	dial             fairwayDialFunc
	now              func() time.Time
}

type fairwayStatusBinary struct {
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Installed bool   `json:"installed"`
}

type fairwayStatusServiceInfo struct {
	Registered bool   `json:"registered"`
	State      string `json:"state,omitempty"`
}

type fairwayStatusDaemon struct {
	Address string `json:"address,omitempty"`
	Uptime  string `json:"uptime,omitempty"`
	Socket  string `json:"socket,omitempty"`
}

type fairwayStatusRoute struct {
	Path   string `json:"path"`
	Auth   string `json:"auth"`
	Action string `json:"action"`
	Calls  int64  `json:"calls"`
}

type fairwayStatusStats struct {
	Total    int64            `json:"total"`
	ByStatus map[string]int64 `json:"byStatus"`
	Errors   int64            `json:"errors"`
}

type fairwayStatusReport struct {
	State         string                   `json:"state"`
	Version       string                   `json:"version,omitempty"`
	Binary        fairwayStatusBinary      `json:"binary"`
	Service       fairwayStatusServiceInfo `json:"service"`
	Daemon        fairwayStatusDaemon      `json:"daemon"`
	Routes        []fairwayStatusRoute     `json:"routes"`
	Stats         fairwayStatusStats       `json:"stats"`
	RouteCount    int                      `json:"-"`
	InFlight      int                      `json:"-"`
	MaxInFlight   int                      `json:"-"`
	VersionAdvice string                   `json:"-"`
}

func newFairwayStatusCmd() *cobra.Command {
	return newFairwayStatusCmdWith(fairwayStatusDeps{})
}

func newFairwayStatusCmdWith(deps fairwayStatusDeps) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show fairway installation, service, and daemon status",
		Long: `Reports whether the fairway daemon is installed, registered as a user service,
and currently running. When the daemon is live, also shows the bound address,
uptime, active route count, and a 24-hour traffic summary. Use --json for
machine-readable output.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := collectFairwayStatus(cmd.Context(), deps)
			if jsonOutput {
				if renderErr := renderFairwayStatusJSON(cmd.OutOrStdout(), report); renderErr != nil {
					return renderErr
				}
			} else {
				renderFairwayStatusHuman(cmd.OutOrStdout(), report)
			}
			if errors.Is(err, errFairwayVersionMismatch) {
				return err
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print status as JSON")
	return cmd
}

func collectFairwayStatus(ctx context.Context, deps fairwayStatusDeps) (fairwayStatusReport, error) {
	deps = deps.withDefaults()

	report := fairwayStatusReport{
		State: "not installed",
		Binary: fairwayStatusBinary{
			Path: deps.binPath,
		},
		Service: fairwayStatusServiceInfo{
			State: "unknown",
		},
		Daemon: fairwayStatusDaemon{
			Socket: deps.socketPath,
		},
		Stats: fairwayStatusStats{
			ByStatus: map[string]int64{},
		},
		Routes: []fairwayStatusRoute{},
	}

	installedVersion, err := deps.installedVersion()
	if err != nil {
		return report, nil
	}
	report.Binary.Installed = true
	report.Binary.Version = parseInstalledVersion(installedVersion)

	svc, err := deps.newService()
	if err != nil {
		report.State = "not registered"
		report.Service.State = "unavailable"
		return report, nil
	}

	record, runtimeStatus, ok := findFairwayService(ctx, svc)
	if !ok {
		report.State = "not registered"
		report.Service.State = "missing"
		return report, nil
	}

	report.Service.Registered = true
	report.Service.State = normalizeServiceState(runtimeStatus.State)
	if report.Service.State == "" {
		report.Service.State = "unknown"
	}

	if !serviceLooksRunning(runtimeStatus.State) {
		report.State = "stopped"
		return report, nil
	}

	client, err := deps.dial(ctx, fairwayctl.Opts{
		SocketPath: deps.socketPath,
		Version:    deps.version,
	})
	if err != nil {
		var vmErr *fairwayctl.ErrVersionMismatch
		switch {
		case errors.As(err, &vmErr):
			report.State = "version mismatch"
			report.Version = vmErr.Daemon
			report.VersionAdvice = "Run `shipyard update` to update the fairway daemon."
			return report, errFairwayVersionMismatch
		case errors.Is(err, fairwayctl.ErrDaemonNotRunning), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			report.State = "stopped"
			return report, nil
		default:
			report.State = "stopped"
			return report, nil
		}
	}
	defer client.Close() //nolint:errcheck

	statusInfo, err := client.Status(ctx)
	if err != nil {
		report.State = "stopped"
		return report, nil
	}
	statsSnap, err := client.Stats(ctx)
	if err != nil {
		report.State = "stopped"
		return report, nil
	}
	routes, err := client.RouteList(ctx)
	if err != nil {
		report.State = "stopped"
		return report, nil
	}

	report.State = "running"
	report.Version = statusInfo.Version
	report.Daemon.Address = netAddress(statusInfo.Bind, statusInfo.Port)
	report.Daemon.Uptime = statusInfo.Uptime
	report.RouteCount = statusInfo.RouteCount
	report.InFlight = statusInfo.InFlight
	if record.Command != "" {
		report.MaxInFlight = extractMaxInFlight(record.Command)
	}
	if report.MaxInFlight == 0 {
		report.MaxInFlight = 16
	}
	report.Stats = buildFairwayStats(statsSnap)
	report.Routes = buildFairwayRoutes(routes, statsSnap)

	return report, nil
}

func (d fairwayStatusDeps) withDefaults() fairwayStatusDeps {
	if d.version == "" {
		d.version = app.Version
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.binPath == "" || d.socketPath == "" || d.installedVersion == nil {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			if d.binPath == "" {
				d.binPath = filepath.Join(homeDir, ".local", "bin", "shipyard-fairway")
			}
			if d.socketPath == "" {
				d.socketPath = filepath.Join(homeDir, ".shipyard", "run", "fairway.sock")
			}
			if d.installedVersion == nil {
				inst := &fairwayctl.Installer{
					Version: d.version,
					BinDir:  filepath.Join(homeDir, ".local", "bin"),
				}
				d.installedVersion = inst.InstalledVersion
			}
		}
	}
	if d.newService == nil {
		d.newService = func() (fairwayStatusService, error) {
			return service.NewService()
		}
	}
	if d.dial == nil {
		d.dial = func(ctx context.Context, opts fairwayctl.Opts) (fairwayStatusClient, error) {
			return fairwayctl.Dial(ctx, opts)
		}
	}
	return d
}

func findFairwayService(_ context.Context, svc fairwayStatusService) (service.ServiceRecord, service.RuntimeStatus, bool) {
	records, err := svc.List()
	if err != nil {
		return service.ServiceRecord{}, service.RuntimeStatus{}, false
	}
	for _, record := range records {
		if record.Name != fairwayServiceName {
			continue
		}
		foundRecord, runtimeStatus, err := svc.Status(record.ID)
		if err != nil {
			return record, service.RuntimeStatus{}, true
		}
		return foundRecord, runtimeStatus, true
	}
	return service.ServiceRecord{}, service.RuntimeStatus{}, false
}

func renderFairwayStatusHuman(w io.Writer, report fairwayStatusReport) {
	ui.Printf(w, "%s\n", ui.SectionTitle("Fairway"))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  State:\t%s\n", paintFairwayState(report.State))
	if report.Version != "" {
		fmt.Fprintf(tw, "  Version:\t%s\n", report.Version)
	}
	if report.Daemon.Uptime != "" {
		fmt.Fprintf(tw, "  Uptime:\t%s\n", report.Daemon.Uptime)
	}
	if report.Daemon.Address != "" {
		fmt.Fprintf(tw, "  Listen:\t%s\n", report.Daemon.Address)
	}
	if report.Daemon.Socket != "" {
		fmt.Fprintf(tw, "  Socket:\t%s\n", report.Daemon.Socket)
	}
	fmt.Fprintf(tw, "  Routes:\t%s\n", formatInt(int64(routeCountForReport(report))))
	if report.State == "running" {
		fmt.Fprintf(tw, "  In-flight:\t%d / %d\n", report.InFlight, report.MaxInFlight)
	}
	_ = tw.Flush()

	if report.VersionAdvice != "" {
		ui.Printf(w, "\n%s\n", ui.Paint(report.VersionAdvice, ui.StyleRed, ui.StyleBold))
	}

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Requests (last 24h)"))
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  Total:\t%s\n", formatInt(report.Stats.Total))
	fmt.Fprintf(tw, "  By status:\t%s\n", formatStatusCounts(report.Stats.ByStatus))
	fmt.Fprintf(tw, "  Errors:\t%s\n", formatInt(report.Stats.Errors))
	_ = tw.Flush()

	ui.Printf(w, "\n%s\n", ui.SectionTitle("Routes"))
	if len(report.Routes) == 0 {
		ui.Printf(w, "  %s\n", ui.Muted("No routes configured."))
		return
	}
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  PATH\tAUTH\tACTION\tCALLS")
	for _, route := range report.Routes {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", route.Path, route.Auth, route.Action, formatInt(route.Calls))
	}
	_ = tw.Flush()
}

func renderFairwayStatusJSON(w io.Writer, report fairwayStatusReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func buildFairwayStats(snapshot fairwayctl.StatsSnapshot) fairwayStatusStats {
	byStatus := make(map[string]int64, len(snapshot.ByStatus))
	for code, count := range snapshot.ByStatus {
		byStatus[strconv.Itoa(code)] = count
	}
	var errorsTotal int64
	for _, routeStats := range snapshot.ByRoute {
		errorsTotal += routeStats.ErrCount
	}
	return fairwayStatusStats{
		Total:    snapshot.Total,
		ByStatus: byStatus,
		Errors:   errorsTotal,
	}
}

func buildFairwayRoutes(routes []fairwayctl.Route, snapshot fairwayctl.StatsSnapshot) []fairwayStatusRoute {
	out := make([]fairwayStatusRoute, 0, len(routes))
	for _, route := range routes {
		routeStats, ok := snapshot.ByRoute[route.Path]
		var calls int64
		if ok {
			calls = routeStats.Count
		}
		out = append(out, fairwayStatusRoute{
			Path:   route.Path,
			Auth:   string(route.Auth.Type),
			Action: formatRouteAction(route.Action),
			Calls:  calls,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func formatRouteAction(action fairwayctl.Action) string {
	if action.Target != "" {
		return string(action.Type) + ":" + action.Target
	}
	if action.URL != "" {
		return string(action.Type) + ":" + action.URL
	}
	return string(action.Type)
}

func routeCountForReport(report fairwayStatusReport) int {
	if report.RouteCount > 0 {
		return report.RouteCount
	}
	return len(report.Routes)
}

func parseInstalledVersion(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "shipyard-fairway ") {
		parts := strings.Fields(trimmed)
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return trimmed
}

func netAddress(bind string, port int) string {
	if bind == "" && port == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", bind, port)
}

func normalizeServiceState(state string) string {
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "active", "running":
		return "active"
	case "inactive", "stopped", "failed":
		return "stopped"
	default:
		return strings.TrimSpace(strings.ToLower(state))
	}
}

func serviceLooksRunning(state string) bool {
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "active", "running":
		return true
	default:
		return false
	}
}

func paintFairwayState(state string) string {
	switch state {
	case "running":
		return ui.Paint(state, ui.StyleBold, ui.StyleCyan)
	case "version mismatch":
		return ui.Paint(state, ui.StyleBold, ui.StyleRed)
	default:
		return state
	}
}

func formatStatusCounts(byStatus map[string]int64) string {
	if len(byStatus) == 0 {
		return "none"
	}
	keys := make([]int, 0, len(byStatus))
	for code := range byStatus {
		parsed, err := strconv.Atoi(code)
		if err != nil {
			continue
		}
		keys = append(keys, parsed)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		code := strconv.Itoa(key)
		parts = append(parts, fmt.Sprintf("%s→%s", code, formatInt(byStatus[code])))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "  ")
}

func formatInt(v int64) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	s := strconv.FormatInt(v, 10)
	if len(s) <= 3 {
		return sign + s
	}
	parts := make([]string, 0, (len(s)+2)/3)
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return sign + strings.Join(parts, ",")
}

func extractMaxInFlight(command string) int {
	for _, token := range strings.Fields(command) {
		if strings.HasPrefix(token, "--max-in-flight=") {
			value := strings.TrimPrefix(token, "--max-in-flight=")
			parsed, err := strconv.Atoi(value)
			if err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}
