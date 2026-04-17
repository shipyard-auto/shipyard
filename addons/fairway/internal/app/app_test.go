package app_test

import (
	"regexp"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/app"
)

func TestInfo(t *testing.T) {
	// Not parallel at the subtest level — subtests mutate package-level vars.
	tests := []struct {
		name      string
		version   string
		commit    string
		buildDate string
		wantInfo  string
	}{
		{
			name:      "Info_reportsDefaults",
			version:   "dev",
			commit:    "unknown",
			buildDate: "unknown",
			wantInfo:  "shipyard-fairway dev (unknown, built unknown)",
		},
		{
			name:      "Info_reportsInjectedValues",
			version:   "0.21",
			commit:    "abc1234",
			buildDate: "2026-04-16T00:00:00Z",
			wantInfo:  "shipyard-fairway 0.21 (abc1234, built 2026-04-16T00:00:00Z)",
		},
		{
			name:      "Info_reportsPartialInjection",
			version:   "1.0",
			commit:    "unknown",
			buildDate: "unknown",
			wantInfo:  "shipyard-fairway 1.0 (unknown, built unknown)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Save and restore package vars around each subtest.
			origVersion, origCommit, origBuildDate := app.Version, app.Commit, app.BuildDate
			t.Cleanup(func() {
				app.Version = origVersion
				app.Commit = origCommit
				app.BuildDate = origBuildDate
			})

			app.Version = tc.version
			app.Commit = tc.commit
			app.BuildDate = tc.buildDate

			got := app.Info()
			if got != tc.wantInfo {
				t.Errorf("Info() = %q; want %q", got, tc.wantInfo)
			}
		})
	}
}

func TestInfo_formatIsStable(t *testing.T) {
	// Format contract: "shipyard-fairway <version> (<commit>, built <buildDate>)"
	pattern := regexp.MustCompile(`^shipyard-fairway \S+ \(\S+, built \S+\)$`)

	cases := [][3]string{
		{"dev", "unknown", "unknown"},
		{"0.21", "abc1234", "2026-04-16T00:00:00Z"},
		{"9.9.9", "deadbeef", "2099-01-01T00:00:00Z"},
	}

	origVersion, origCommit, origBuildDate := app.Version, app.Commit, app.BuildDate
	t.Cleanup(func() {
		app.Version = origVersion
		app.Commit = origCommit
		app.BuildDate = origBuildDate
	})

	for _, c := range cases {
		app.Version, app.Commit, app.BuildDate = c[0], c[1], c[2]

		got := app.Info()
		if !pattern.MatchString(got) {
			t.Errorf("Info() format broken for version=%q: got %q", c[0], got)
		}
	}
}
