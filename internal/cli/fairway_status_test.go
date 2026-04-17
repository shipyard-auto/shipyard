package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/service"
)

type fakeFairwayStatusService struct {
	records   []service.ServiceRecord
	statuses  map[string]service.RuntimeStatus
	listErr   error
	statusErr error
}

func (f *fakeFairwayStatusService) List() ([]service.ServiceRecord, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.records, nil
}

func (f *fakeFairwayStatusService) Status(id string) (service.ServiceRecord, service.RuntimeStatus, error) {
	if f.statusErr != nil {
		return service.ServiceRecord{}, service.RuntimeStatus{}, f.statusErr
	}
	for _, record := range f.records {
		if record.ID == id {
			return record, f.statuses[id], nil
		}
	}
	return service.ServiceRecord{}, service.RuntimeStatus{}, service.ErrServiceNotFound
}

type fakeFairwayStatusClient struct {
	status    fairwayctl.StatusInfo
	stats     fairwayctl.StatsSnapshot
	routes    []fairwayctl.Route
	statusErr error
	statsErr  error
	routesErr error
}

func (f *fakeFairwayStatusClient) Close() error { return nil }

func (f *fakeFairwayStatusClient) RouteList(context.Context) ([]fairwayctl.Route, error) {
	if f.routesErr != nil {
		return nil, f.routesErr
	}
	return f.routes, nil
}

func (f *fakeFairwayStatusClient) Status(context.Context) (fairwayctl.StatusInfo, error) {
	if f.statusErr != nil {
		return fairwayctl.StatusInfo{}, f.statusErr
	}
	return f.status, nil
}

func (f *fakeFairwayStatusClient) Stats(context.Context) (fairwayctl.StatsSnapshot, error) {
	if f.statsErr != nil {
		return fairwayctl.StatsSnapshot{}, f.statsErr
	}
	return f.stats, nil
}

func testFairwayStatusDeps() fairwayStatusDeps {
	return fairwayStatusDeps{
		binPath:    "/tmp/shipyard-fairway",
		socketPath: "/tmp/fairway.sock",
		version:    "0.21",
		installedVersion: func() (string, error) {
			return "shipyard-fairway 0.21 (deadbeef, built now)", nil
		},
		newService: func() (fairwayStatusService, error) {
			return &fakeFairwayStatusService{
				records: []service.ServiceRecord{
					{
						ID:      "FW1234",
						Name:    "fairway",
						Command: "/tmp/shipyard-fairway --config /tmp/routes.json --max-in-flight=16",
					},
				},
				statuses: map[string]service.RuntimeStatus{
					"FW1234": {State: "active"},
				},
			}, nil
		},
		dial: func(context.Context, fairwayctl.Opts) (fairwayStatusClient, error) {
			return &fakeFairwayStatusClient{
				status: fairwayctl.StatusInfo{
					Version:    "0.21",
					Uptime:     "3h12m",
					Port:       9876,
					Bind:       "127.0.0.1",
					RouteCount: 4,
					InFlight:   0,
				},
				stats: fairwayctl.StatsSnapshot{
					Total: 1842,
					ByRoute: map[string]fairwayctl.RouteStats{
						"/hooks/github":    {Count: 1512, ErrCount: 21},
						"/hooks/telegram":  {Count: 330},
						"/hooks/grafana":   {Count: 0},
						"/internal/events": {Count: 0},
					},
					ByStatus: map[int]int64{
						200: 1801,
						401: 20,
						500: 15,
						504: 6,
					},
				},
				routes: []fairwayctl.Route{
					{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "ABC123"}},
					{Path: "/hooks/telegram", Auth: fairwayctl.Auth{Type: fairwayctl.AuthToken}, Action: fairwayctl.Action{Type: fairwayctl.ActionTelegramHandle, Target: "telegram.handle"}},
					{Path: "/internal/events", Auth: fairwayctl.Auth{Type: fairwayctl.AuthLocalOnly}, Action: fairwayctl.Action{Type: fairwayctl.ActionMessageSend, Target: "event.dispatch"}},
					{Path: "/hooks/grafana", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionMessageSend, Target: "message.send"}},
				},
			}, nil
		},
		now: func() time.Time {
			return time.Date(2026, 4, 17, 13, 0, 0, 0, time.UTC)
		},
	}
}

func TestStatus_notInstalled(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	deps := testFairwayStatusDeps()
	deps.installedVersion = func() (string, error) { return "", errors.New("missing binary") }

	report, err := collectFairwayStatus(context.Background(), deps)
	if err != nil {
		t.Fatalf("collectFairwayStatus() error = %v", err)
	}
	if report.State != "not installed" {
		t.Fatalf("state = %q; want %q", report.State, "not installed")
	}
}

func TestStatus_notRegistered(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	deps := testFairwayStatusDeps()
	deps.newService = func() (fairwayStatusService, error) {
		return &fakeFairwayStatusService{}, nil
	}

	report, err := collectFairwayStatus(context.Background(), deps)
	if err != nil {
		t.Fatalf("collectFairwayStatus() error = %v", err)
	}
	if report.State != "not registered" {
		t.Fatalf("state = %q; want %q", report.State, "not registered")
	}
}

func TestStatus_stopped(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	deps := testFairwayStatusDeps()
	deps.newService = func() (fairwayStatusService, error) {
		return &fakeFairwayStatusService{
			records: []service.ServiceRecord{{ID: "FW1234", Name: "fairway"}},
			statuses: map[string]service.RuntimeStatus{
				"FW1234": {State: "inactive"},
			},
		}, nil
	}

	report, err := collectFairwayStatus(context.Background(), deps)
	if err != nil {
		t.Fatalf("collectFairwayStatus() error = %v", err)
	}
	if report.State != "stopped" {
		t.Fatalf("state = %q; want %q", report.State, "stopped")
	}
}

func TestStatus_versionMismatch(t *testing.T) {
	oldTerm := os.Getenv("TERM")
	t.Cleanup(func() { _ = os.Setenv("TERM", oldTerm) })
	_ = os.Setenv("TERM", "xterm-256color")
	_ = os.Unsetenv("NO_COLOR")

	deps := testFairwayStatusDeps()
	deps.dial = func(context.Context, fairwayctl.Opts) (fairwayStatusClient, error) {
		return nil, &fairwayctl.ErrVersionMismatch{Daemon: "0.22", Client: "0.21"}
	}

	cmd := newFairwayStatusCmdWith(deps)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if !errors.Is(err, errFairwayVersionMismatch) {
		t.Fatalf("Execute() error = %v; want version mismatch", err)
	}
	out := buf.String()
	if !strings.Contains(out, "version mismatch") {
		t.Fatalf("output = %q; want version mismatch", out)
	}
	if !strings.Contains(out, "shipyard fairway upgrade") {
		t.Fatalf("output = %q; want upgrade suggestion", out)
	}
	if !strings.Contains(out, "\033[31m") {
		t.Fatalf("output = %q; want red ANSI sequence", out)
	}
}

func TestStatus_running_humanOutput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	report, err := collectFairwayStatus(context.Background(), testFairwayStatusDeps())
	if err != nil {
		t.Fatalf("collectFairwayStatus() error = %v", err)
	}

	buf := &bytes.Buffer{}
	renderFairwayStatusHuman(buf, report)

	const want = "" +
		"Fairway\n" +
		"  State:      running\n" +
		"  Version:    0.21\n" +
		"  Uptime:     3h12m\n" +
		"  Listen:     127.0.0.1:9876\n" +
		"  Socket:     /tmp/fairway.sock\n" +
		"  Routes:     4\n" +
		"  In-flight:  0 / 16\n" +
		"\n" +
		"Requests (last 24h)\n" +
		"  Total:      1,842\n" +
		"  By status:  200→1,801  401→20  500→15  504→6\n" +
		"  Errors:     21\n" +
		"\n" +
		"Routes\n" +
		"  PATH              AUTH        ACTION                           CALLS\n" +
		"  /hooks/github     bearer      cron.run:ABC123                  1,512\n" +
		"  /hooks/grafana    bearer      message.send:message.send        0\n" +
		"  /hooks/telegram   token       telegram.handle:telegram.handle  330\n" +
		"  /internal/events  local-only  message.send:event.dispatch      0\n"

	if got := buf.String(); got != want {
		t.Fatalf("human output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestStatus_running_jsonOutput(t *testing.T) {
	deps := testFairwayStatusDeps()
	cmd := newFairwayStatusCmdWith(deps)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("Flags().Set(json): %v", err)
	}

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var got fairwayStatusReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\n%s", err, buf.String())
	}
	if got.State != "running" {
		t.Fatalf("state = %q; want running", got.State)
	}
	if got.Service.State != "active" {
		t.Fatalf("service.state = %q; want active", got.Service.State)
	}
	if got.Daemon.Address != "127.0.0.1:9876" {
		t.Fatalf("daemon.address = %q", got.Daemon.Address)
	}
	if got.Stats.Total != 1842 {
		t.Fatalf("stats.total = %d; want 1842", got.Stats.Total)
	}
	if len(got.Routes) != 4 {
		t.Fatalf("len(routes) = %d; want 4", len(got.Routes))
	}
}

func TestStatus_emptyStats_handlesGracefully(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	report := fairwayStatusReport{
		State: "running",
		Daemon: fairwayStatusDaemon{
			Socket: "/tmp/fairway.sock",
		},
		Stats: fairwayStatusStats{
			ByStatus: map[string]int64{},
		},
		Routes:      []fairwayStatusRoute{},
		MaxInFlight: 16,
	}

	buf := &bytes.Buffer{}
	renderFairwayStatusHuman(buf, report)
	out := buf.String()
	if !strings.Contains(out, "Total:      0") {
		t.Fatalf("output = %q; want zero total", out)
	}
	if !strings.Contains(out, "By status:  none") {
		t.Fatalf("output = %q; want empty by status", out)
	}
	if !strings.Contains(out, "No routes configured.") {
		t.Fatalf("output = %q; want empty routes message", out)
	}
}

func TestStatus_connectionTimeout_degradesGracefully(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	deps := testFairwayStatusDeps()
	deps.dial = func(context.Context, fairwayctl.Opts) (fairwayStatusClient, error) {
		return nil, context.DeadlineExceeded
	}

	cmd := newFairwayStatusCmdWith(deps)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(buf.String(), "stopped") {
		t.Fatalf("output = %q; want stopped", buf.String())
	}
}

func TestStatus_exitCode1_onVersionMismatch(t *testing.T) {
	deps := testFairwayStatusDeps()
	deps.dial = func(context.Context, fairwayctl.Opts) (fairwayStatusClient, error) {
		return nil, &fairwayctl.ErrVersionMismatch{Daemon: "0.22", Client: "0.21"}
	}

	err := newFairwayStatusCmdWith(deps).Execute()
	if !errors.Is(err, errFairwayVersionMismatch) {
		t.Fatalf("Execute() error = %v; want version mismatch", err)
	}
}

func TestStatus_exitCode0_onStopped(t *testing.T) {
	deps := testFairwayStatusDeps()
	deps.newService = func() (fairwayStatusService, error) {
		return &fakeFairwayStatusService{
			records: []service.ServiceRecord{{ID: "FW1234", Name: "fairway"}},
			statuses: map[string]service.RuntimeStatus{
				"FW1234": {State: "inactive"},
			},
		}, nil
	}

	if err := newFairwayStatusCmdWith(deps).Execute(); err != nil {
		t.Fatalf("Execute() error = %v; want nil", err)
	}
}

func TestStatus_serviceUnavailable_degradesGracefully(t *testing.T) {
	deps := testFairwayStatusDeps()
	deps.newService = func() (fairwayStatusService, error) {
		return nil, errors.New("service init failed")
	}

	report, err := collectFairwayStatus(context.Background(), deps)
	if err != nil {
		t.Fatalf("collectFairwayStatus() error = %v", err)
	}
	if report.State != "not registered" {
		t.Fatalf("state = %q; want not registered", report.State)
	}
	if report.Service.State != "unavailable" {
		t.Fatalf("service.state = %q; want unavailable", report.Service.State)
	}
}

func TestStatus_rpcFailure_degradesGracefully(t *testing.T) {
	deps := testFairwayStatusDeps()
	deps.dial = func(context.Context, fairwayctl.Opts) (fairwayStatusClient, error) {
		return &fakeFairwayStatusClient{statusErr: errors.New("boom")}, nil
	}

	report, err := collectFairwayStatus(context.Background(), deps)
	if err != nil {
		t.Fatalf("collectFairwayStatus() error = %v", err)
	}
	if report.State != "stopped" {
		t.Fatalf("state = %q; want stopped", report.State)
	}
}

func TestFairwayStatusDeps_withDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	deps := (fairwayStatusDeps{}).withDefaults()
	if deps.version == "" {
		t.Fatal("version should be defaulted")
	}
	if deps.binPath != filepath.Join(home, ".local", "bin", "shipyard-fairway") {
		t.Fatalf("binPath = %q", deps.binPath)
	}
	if deps.socketPath != filepath.Join(home, ".shipyard", "run", "fairway.sock") {
		t.Fatalf("socketPath = %q", deps.socketPath)
	}
	if deps.installedVersion == nil || deps.newService == nil || deps.dial == nil || deps.now == nil {
		t.Fatal("expected all defaulted functions to be set")
	}
}

func TestFindFairwayService_handlesErrors(t *testing.T) {
	_, _, ok := findFairwayService(context.Background(), &fakeFairwayStatusService{listErr: errors.New("boom")})
	if ok {
		t.Fatal("expected not found on list error")
	}

	record, _, ok := findFairwayService(context.Background(), &fakeFairwayStatusService{
		records:   []service.ServiceRecord{{ID: "FW1234", Name: "fairway"}},
		statusErr: errors.New("status boom"),
	})
	if !ok {
		t.Fatal("expected match despite status error")
	}
	if record.ID != "FW1234" {
		t.Fatalf("record.ID = %q; want FW1234", record.ID)
	}
}

func TestFairwayStatusHelpers(t *testing.T) {
	if got := formatRouteAction(fairwayctl.Action{Type: fairwayctl.ActionHTTPForward, URL: "https://example.com"}); got != "http.forward:https://example.com" {
		t.Fatalf("formatRouteAction(url) = %q", got)
	}
	if got := formatRouteAction(fairwayctl.Action{Type: fairwayctl.ActionServiceRestart}); got != "service.restart" {
		t.Fatalf("formatRouteAction(type) = %q", got)
	}
	if got := parseInstalledVersion("0.30"); got != "0.30" {
		t.Fatalf("parseInstalledVersion(raw) = %q", got)
	}
	if got := netAddress("", 0); got != "" {
		t.Fatalf("netAddress(empty) = %q", got)
	}
	if got := normalizeServiceState("running"); got != "active" {
		t.Fatalf("normalizeServiceState(running) = %q", got)
	}
	if got := normalizeServiceState("failed"); got != "stopped" {
		t.Fatalf("normalizeServiceState(failed) = %q", got)
	}
	if got := normalizeServiceState("weird"); got != "weird" {
		t.Fatalf("normalizeServiceState(weird) = %q", got)
	}
}

func TestNewFairwayStatusCmd_registersJSONFlag(t *testing.T) {
	cmd := newFairwayStatusCmd()
	if cmd == nil {
		t.Fatal("newFairwayStatusCmd() returned nil")
	}
	flag := cmd.Flags().Lookup("json")
	if flag == nil {
		t.Fatal("expected --json flag to be registered")
	}
}
