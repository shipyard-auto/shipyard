package app

import "testing"

func TestInfo(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		commit    string
		buildDate string
		want      string
	}{
		{"all defaults", "dev", "unknown", "unknown", "shipyard-crew dev (unknown, built unknown)"},
		{"custom values", "1.2.3", "abc1234", "2026-04-20", "shipyard-crew 1.2.3 (abc1234, built 2026-04-20)"},
		{"mix partial", "1.2.3", "unknown", "unknown", "shipyard-crew 1.2.3 (unknown, built unknown)"},
	}

	origV, origC, origB := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = origV, origC, origB })

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Version, Commit, BuildDate = tc.version, tc.commit, tc.buildDate
			got := Info()
			if got != tc.want {
				t.Fatalf("Info() = %q, want %q", got, tc.want)
			}
		})
	}
}
