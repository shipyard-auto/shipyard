// socket_version_internal_test.go — package-internal tests for compatibleVersion
// and parseMajor, which are unexported helpers used by the handshake flow to
// allow the fairway daemon and shipyard core to evolve independently within a
// major version.
package fairway

import "testing"

func TestCompatibleVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		server string
		client string
		want   bool
	}{
		{name: "identical", server: "1.1.7", client: "1.1.7", want: true},
		{name: "minor drift same major", server: "1.1.5", client: "1.1.7", want: true},
		{name: "patch drift same major", server: "1.1.7", client: "1.1.0", want: true},
		{name: "major drift 0 to 1", server: "0.9.0", client: "1.0.0", want: false},
		{name: "major drift 1 to 2", server: "1.9.9", client: "2.0.0", want: false},
		{name: "v prefix client", server: "1.1.7", client: "v1.2.0", want: true},
		{name: "v prefix both", server: "v1.1.7", client: "v1.2.0", want: true},
		{name: "prerelease same major", server: "1.1.7", client: "1.2.0-rc1", want: true},
		{name: "non semver identical falls back to equality", server: "dev", client: "dev", want: true},
		{name: "non semver different falls back to inequality", server: "daemon-v2", client: "client-v1", want: false},
		{name: "empty server", server: "", client: "1.1.7", want: false},
		{name: "empty client", server: "1.1.7", client: "", want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := compatibleVersion(tc.server, tc.client); got != tc.want {
				t.Errorf("compatibleVersion(%q, %q) = %v; want %v", tc.server, tc.client, got, tc.want)
			}
		})
	}
}

func TestParseMajor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in     string
		wantN  int
		wantOK bool
	}{
		{in: "1.1.7", wantN: 1, wantOK: true},
		{in: "v1.1.7", wantN: 1, wantOK: true},
		{in: "2.0.0-rc1", wantN: 2, wantOK: true},
		{in: "0", wantN: 0, wantOK: true},
		{in: "  3.4  ", wantN: 3, wantOK: true},
		{in: "", wantN: 0, wantOK: false},
		{in: "dev", wantN: 0, wantOK: false},
		{in: "v", wantN: 0, wantOK: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			n, ok := parseMajor(tc.in)
			if n != tc.wantN || ok != tc.wantOK {
				t.Errorf("parseMajor(%q) = (%d, %v); want (%d, %v)", tc.in, n, ok, tc.wantN, tc.wantOK)
			}
		})
	}
}
