package service

import (
	"strings"
	"testing"
)

func TestRenderSystemdUnit(t *testing.T) {
	unit := renderSystemdUnit(withDefaultEnvironment(ServiceRecord{
		ID: "AB12CD", Name: "Heartbeat", Description: "Writes pulse", Command: "echo 'ok'",
		WorkingDir: "/tmp", Environment: map[string]string{"FOO": "BAR"}, AutoRestart: true,
	}, "/home/tester"))
	for _, want := range []string{
		"# id=AB12CD",
		"Description=Heartbeat - Writes pulse",
		"Restart=on-failure",
		"WorkingDirectory=/tmp",
		`Environment="FOO=BAR"`,
		`Environment="HOME=/home/tester"`,
		`ExecStart=/bin/sh -lc 'echo '\''ok'\'''`,
		"StandardOutput=append:/tmp/shipyard-AB12CD.out.log",
		"StandardError=append:/tmp/shipyard-AB12CD.err.log",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("expected %q in unit:\n%s", want, unit)
		}
	}
}

func TestRenderLaunchdPlist(t *testing.T) {
	plist := renderLaunchdPlist(withDefaultEnvironment(ServiceRecord{
		ID: "AB12CD", Name: "Heartbeat", Command: "echo & <ok>", Enabled: true, AutoRestart: true,
		WorkingDir: "/tmp", Environment: map[string]string{"FOO": "BAR"},
	}, "/Users/tester"))
	for _, want := range []string{
		"<string>com.shipyard.service.AB12CD</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>WorkingDirectory</key>",
		"<key>EnvironmentVariables</key>",
		"<key>HOME</key>",
		"<string>/Users/tester</string>",
		"<string>/tmp/shipyard-AB12CD.out.log</string>",
		"<string>/tmp/shipyard-AB12CD.err.log</string>",
		"&amp;",
		"&lt;ok&gt;",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("expected %q in plist:\n%s", want, plist)
		}
	}
}
