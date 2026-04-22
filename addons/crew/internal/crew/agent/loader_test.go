package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_Good(t *testing.T) {
	a, err := Load("testdata/good")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Name != "promo-hunter" {
		t.Errorf("name=%q", a.Name)
	}
	if a.Dir != "testdata/good" {
		t.Errorf("dir=%q", a.Dir)
	}
	if a.PromptPath != filepath.Join("testdata/good", "prompt.md") {
		t.Errorf("promptPath=%q", a.PromptPath)
	}
	if len(a.Tools) != 2 {
		t.Errorf("tools=%d", len(a.Tools))
	}
	if len(a.Triggers) != 1 || a.Triggers[0].Type != "cron" {
		t.Errorf("triggers=%+v", a.Triggers)
	}
}

func TestLoad_MissingPrompt(t *testing.T) {
	_, err := Load("testdata/missing-prompt")
	if err == nil || !strings.Contains(err.Error(), "prompt.md") {
		t.Fatalf("want prompt.md error, got %v", err)
	}
}

func TestLoad_InvalidValidation(t *testing.T) {
	_, err := Load("testdata/invalid-validation")
	if err == nil || !strings.Contains(err.Error(), "validate") {
		t.Fatalf("want validate error, got %v", err)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	_, err := Load("testdata/malformed-yaml")
	if err == nil || !strings.Contains(err.Error(), "parse agent.yaml") {
		t.Fatalf("want parse error, got %v", err)
	}
}

func TestLoad_BadSchemaVersion(t *testing.T) {
	_, err := Load("testdata/bad-schema-version")
	if err == nil || !strings.Contains(err.Error(), "unsupported schema_version") {
		t.Fatalf("want schema version error, got %v", err)
	}
}

func TestLoad_NonExistentDir(t *testing.T) {
	_, err := Load("testdata/does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "read agent.yaml") {
		t.Fatalf("want read error, got %v", err)
	}
}

func TestLoad_UnknownField(t *testing.T) {
	dir := t.TempDir()
	yaml := `schema_version: "1"
name: u
description: ""
backend:
  type: cli
  command: ["x"]
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateless
triggers: []
tools: []
foo: bar
`
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("p"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "parse agent.yaml") {
		t.Fatalf("want parse error from unknown field, got %v", err)
	}
}
