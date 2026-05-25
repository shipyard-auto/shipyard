package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/app"
)

func TestRun_PrintsHeader(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "shipyard-comms") {
		t.Errorf("expected 'shipyard-comms' in stdout, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "(stub)") {
		t.Errorf("expected '(stub)' marker in stdout, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr must be empty, got %q", stderr.String())
	}
}

func TestRun_VersionFlag(t *testing.T) {
	for _, flag := range []string{"--version", "-v"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run([]string{flag}, &stdout, &stderr); err != nil {
				t.Fatalf("run: %v", err)
			}
			got := strings.TrimSpace(stdout.String())
			if got != app.Version {
				t.Errorf("got %q, want %q", got, app.Version)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr must be empty, got %q", stderr.String())
			}
		})
	}
}
