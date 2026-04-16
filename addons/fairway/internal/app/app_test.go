package app

import (
	"regexp"
	"testing"
)

func TestInfo(t *testing.T) {
	originalVersion := Version
	originalCommit := Commit
	originalBuildDate := BuildDate
	t.Cleanup(func() {
		Version = originalVersion
		Commit = originalCommit
		BuildDate = originalBuildDate
	})

	tests := []struct {
		name      string
		version   string
		commit    string
		buildDate string
		want      string
		match     *regexp.Regexp
	}{
		{
			name:      "Info_reportsDefaults",
			version:   "dev",
			commit:    "unknown",
			buildDate: "unknown",
			want:      "shipyard-fairway dev (unknown, built unknown)",
		},
		{
			name:      "Info_reportsInjectedValues",
			version:   "9.9.9",
			commit:    "abc1234",
			buildDate: "2026-04-16T00:00:00Z",
			want:      "shipyard-fairway 9.9.9 (abc1234, built 2026-04-16T00:00:00Z)",
		},
		{
			name:      "Info_formatIsStable",
			version:   "1.2.3",
			commit:    "deadbeef",
			buildDate: "2026-04-16",
			want:      "shipyard-fairway 1.2.3 (deadbeef, built 2026-04-16)",
			match:     regexp.MustCompile(`^shipyard-fairway \S+ \(\S+, built .+\)$`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version = tt.version
			Commit = tt.commit
			BuildDate = tt.buildDate

			got := Info()
			if got != tt.want {
				t.Fatalf("Info() = %q, want %q", got, tt.want)
			}
			if tt.match != nil && !tt.match.MatchString(got) {
				t.Fatalf("Info() = %q, does not match %q", got, tt.match.String())
			}
		})
	}
}
