package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/app"
)

func TestRun_PrintsHeader(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), nil, nil, &stdout, &stderr); err != nil {
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
			if err := run(context.Background(), []string{flag}, nil, &stdout, &stderr); err != nil {
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

func TestRun_UnknownSubcommand_FallsBackToHeader(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"orbit"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "shipyard-comms") {
		t.Errorf("expected header fallback, got %q", stdout.String())
	}
}

func TestRun_SubcommandVersionFlagDoesNotHijack(t *testing.T) {
	// `shipyard-comms send --version` should NOT short-circuit to printing
	// the bare version — the version handler stops scanning at the first
	// non-flag token (the subcommand). We don't need to actually execute
	// send here; we just verify that the top-level scanner left the
	// subcommand alone (the resulting error from send is the marker).
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"send", "--version"}, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected send to error (missing required flags), got nil")
	}
	// The bare version line should NOT be the only thing on stdout.
	if strings.TrimSpace(stdout.String()) == app.Version {
		t.Errorf("--version after subcommand was hijacked by top-level handler")
	}
}

func TestRunChannel_PrintsHelpOnNoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runChannel(context.Background(), nil, nil, &stdout, &stderr); err != nil {
		t.Fatalf("runChannel: %v", err)
	}
	if !strings.Contains(stdout.String(), "Usage: shipyard-comms channel") {
		t.Errorf("expected help text, got %q", stdout.String())
	}
}

func TestRunChannel_UnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runChannel(context.Background(), []string{"orbit"}, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "orbit") {
		t.Errorf("error should name the unknown subcommand, got %v", err)
	}
}

func TestNewChannelID_Format(t *testing.T) {
	id, err := newChannelID()
	if err != nil {
		t.Fatalf("newChannelID: %v", err)
	}
	if len(id) != 32 {
		t.Errorf("id length = %d, want 32", len(id))
	}
	for i, c := range id {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isHex {
			t.Errorf("id[%d] = %q, expected lowercase hex", i, c)
		}
	}
}

func TestNewChannelID_Unique(t *testing.T) {
	const n = 50
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id, err := newChannelID()
		if err != nil {
			t.Fatalf("newChannelID: %v", err)
		}
		if seen[id] {
			t.Errorf("duplicate id after %d generations: %s", i, id)
		}
		seen[id] = true
	}
}
