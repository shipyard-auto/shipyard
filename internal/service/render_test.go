package service

import (
	"strings"
	"testing"
)

func TestRenderSystemdUnit(t *testing.T) {
	unit := renderSystemdUnit(ServiceRecord{
		ID: "AB12CD", Name: "Heartbeat", Description: "Writes pulse", Command: "echo 'ok'",
		WorkingDir: "/tmp", Environment: map[string]string{"FOO": "BAR"}, AutoRestart: true,
	})
	for _, want := range []string{
		"# id=AB12CD",
		"Description=Heartbeat - Writes pulse",
		"Restart=on-failure",
		"WorkingDirectory=/tmp",
		`Environment="FOO=BAR"`,
		`ExecStart=/bin/sh -lc 'echo '\''ok'\'''`,
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("expected %q in unit:\n%s", want, unit)
		}
	}
}

func TestRenderLaunchdPlist(t *testing.T) {
	plist := renderLaunchdPlist(ServiceRecord{
		ID: "AB12CD", Name: "Heartbeat", Command: "echo & <ok>", Enabled: true, AutoRestart: true,
		WorkingDir: "/tmp", Environment: map[string]string{"FOO": "BAR"},
	})
	for _, want := range []string{
		"<string>com.shipyard.service.AB12CD</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>WorkingDirectory</key>",
		"<key>EnvironmentVariables</key>",
		"&amp;",
		"&lt;ok&gt;",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("expected %q in plist:\n%s", want, plist)
		}
	}
}

