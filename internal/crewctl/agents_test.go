package crewctl

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAgent(t *testing.T, home, dir, yamlBody string) {
	t.Helper()
	p := filepath.Join(home, "crew", dir)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "agent.yaml"), []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListAgents_returnsSortedAgents(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "zeta", "name: zeta\ndescription: Z\nbackend: {type: claude}\nexecution: {mode: on-demand}\n")
	writeAgent(t, home, "alpha", "name: alpha\nbackend: {type: gemini}\nexecution: {mode: service, pool: default}\ntriggers:\n  - type: webhook\n  - type: webhook\n  - type: cron\n")

	got, err := ListAgents(home)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d agents, want 2", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("not sorted: %+v", got)
	}
	if got[0].Mode != "service" || got[0].Pool != "default" {
		t.Errorf("alpha fields wrong: %+v", got[0])
	}
	if len(got[0].Triggers) != 2 {
		t.Errorf("expected deduped triggers [webhook cron], got %v", got[0].Triggers)
	}
}

func TestListAgents_missingCrewDirReturnsNil(t *testing.T) {
	home := t.TempDir()
	got, err := ListAgents(home)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}

func TestListAgents_skipsDirsWithoutAgentYAML(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "crew", "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgent(t, home, "ok", "name: ok\nbackend: {type: claude}\nexecution: {mode: on-demand}\n")

	got, err := ListAgents(home)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 1 || got[0].Name != "ok" {
		t.Errorf("expected only ok, got %+v", got)
	}
}

func TestListAgents_fallbackNameFromDir(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "fallback", "backend: {type: claude}\nexecution: {mode: on-demand}\n")

	got, err := ListAgents(home)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 1 || got[0].Name != "fallback" {
		t.Errorf("expected fallback name, got %+v", got)
	}
}
