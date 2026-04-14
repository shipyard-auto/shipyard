package update

import "testing"

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		left  string
		right string
		want  int
	}{
		{name: "equal", left: "0.5", right: "0.5", want: 0},
		{name: "left older", left: "0.5", right: "0.6", want: -1},
		{name: "left newer", left: "0.10", right: "0.9", want: 1},
		{name: "three-part newer", left: "1.2.1", right: "1.2.0", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := compareVersions(tt.left, tt.right); got != tt.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", tt.left, tt.right, got, tt.want)
			}
		})
	}
}

func TestNormalizeVersion(t *testing.T) {
	t.Parallel()

	if got := normalizeVersion("v0.5"); got != "0.5" {
		t.Fatalf("normalizeVersion() = %q, want %q", got, "0.5")
	}
}
