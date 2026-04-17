package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
)

type fakeFairwayStatsClient struct {
	snap fairwayctl.StatsSnapshot
	err  error
}

func (f *fakeFairwayStatsClient) Close() error { return nil }
func (f *fakeFairwayStatsClient) Stats(context.Context) (fairwayctl.StatsSnapshot, error) {
	if f.err != nil {
		return fairwayctl.StatsSnapshot{}, f.err
	}
	return f.snap, nil
}

func statsDeps(client fairwayStatsClient, err error) fairwayStatsDeps {
	return fairwayStatsDeps{
		version:    "0.21",
		socketPath: "/tmp/fairway.sock",
		dial: func(context.Context, fairwayctl.Opts) (fairwayStatsClient, error) {
			if err != nil {
				return nil, err
			}
			return client, nil
		},
	}
}

func sampleSnapshot() fairwayctl.StatsSnapshot {
	return fairwayctl.StatsSnapshot{
		Total: 100,
		ByRoute: map[string]fairwayctl.RouteStats{
			"/a": {Count: 70, ErrCount: 2, LastAt: time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)},
			"/b": {Count: 20, ErrCount: 8, LastAt: time.Date(2026, 4, 17, 11, 0, 0, 0, time.UTC)},
			"/c": {Count: 10, ErrCount: 0, LastAt: time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)},
		},
		ByStatus: map[int]int64{200: 85, 401: 5, 500: 10},
	}
}

func TestStats_default_rendersSummary(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cmd := newFairwayStatsCmdWith(statsDeps(&fakeFairwayStatsClient{snap: sampleSnapshot()}, nil))
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Total:") || !strings.Contains(got, "Error rate:") || !strings.Contains(got, "/a") {
		t.Fatalf("output = %q", got)
	}
}

func TestStats_byRoute_rendersTable(t *testing.T) {
	cmd := newFairwayStatsCmdWith(statsDeps(&fakeFairwayStatsClient{snap: sampleSnapshot()}, nil))
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	mustSet(t, cmd, "by-route", "true")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "PATH") || !strings.Contains(out.String(), "/b") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestStats_byStatus_rendersDistribution(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cmd := newFairwayStatsCmdWith(statsDeps(&fakeFairwayStatsClient{snap: sampleSnapshot()}, nil))
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	mustSet(t, cmd, "by-status", "true")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "STATUS") || !strings.Contains(out.String(), "500") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestStats_json_fullSnapshot(t *testing.T) {
	cmd := newFairwayStatsCmdWith(statsDeps(&fakeFairwayStatsClient{snap: sampleSnapshot()}, nil))
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	mustSet(t, cmd, "json", "true")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var snap fairwayctl.StatsSnapshot
	if err := json.Unmarshal(out.Bytes(), &snap); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if snap.Total != 100 || snap.ByStatus[500] != 10 {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestStats_daemonOffline_errorClear(t *testing.T) {
	cmd := newFairwayStatsCmdWith(statsDeps(nil, fairwayctl.ErrDaemonNotRunning))
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("error = %v", err)
	}
}

func TestStats_emptyStats_rendersZeros(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cmd := newFairwayStatsCmdWith(statsDeps(&fakeFairwayStatsClient{snap: fairwayctl.StatsSnapshot{ByRoute: map[string]fairwayctl.RouteStats{}, ByStatus: map[int]int64{}}}, nil))
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Total:") || !strings.Contains(got, "0") || !strings.Contains(got, "No route traffic yet.") {
		t.Fatalf("output = %q", got)
	}
}

func TestStats_nonOfflineError_passthrough(t *testing.T) {
	cmd := newFairwayStatsCmdWith(statsDeps(nil, errors.New("boom")))
	err := cmd.Execute()
	if err == nil || err.Error() != "boom" {
		t.Fatalf("error = %v", err)
	}
}
