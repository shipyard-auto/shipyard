package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

func sampleAgent(dir string) *crew.Agent {
	return &crew.Agent{
		Name:        "sample",
		Description: "d",
		Backend: crew.Backend{
			Type:    crew.BackendCLI,
			Command: []string{"claude"},
		},
		Execution: crew.Execution{
			Mode: crew.ExecutionOnDemand,
			Pool: "cli",
		},
		Conversation: crew.Conversation{
			Mode: crew.ConversationStateless,
		},
		Triggers: []crew.Trigger{
			{Type: crew.TriggerCron, Schedule: "* * * * *"},
		},
		Tools: []crew.Tool{
			{Name: "t1", Protocol: crew.ToolExec, Command: []string{"/bin/true"}},
		},
		Dir: dir,
	}
}

func TestWrite_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := sampleAgent(dir)
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("p"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(a); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Name != a.Name {
		t.Errorf("name mismatch")
	}
	if len(loaded.Tools) != 1 || loaded.Tools[0].Name != "t1" {
		t.Errorf("tools mismatch: %+v", loaded.Tools)
	}
	if loaded.Triggers[0].Schedule != "* * * * *" {
		t.Errorf("schedule mismatch")
	}
}

func TestWrite_NilAgent(t *testing.T) {
	err := Write(nil)
	if err == nil || !strings.Contains(err.Error(), "nil agent") {
		t.Fatalf("want nil agent error, got %v", err)
	}
}

func TestWrite_EmptyDir(t *testing.T) {
	a := sampleAgent("")
	err := Write(a)
	if err == nil || !strings.Contains(err.Error(), "empty Dir") {
		t.Fatalf("want empty Dir error, got %v", err)
	}
}

func TestWrite_ValidationFails(t *testing.T) {
	dir := t.TempDir()
	a := sampleAgent(dir)
	a.Name = ""
	err := Write(a)
	if err == nil || !strings.Contains(err.Error(), "validate") {
		t.Fatalf("want validate error, got %v", err)
	}
}

func TestWrite_FilePerms(t *testing.T) {
	dir := t.TempDir()
	a := sampleAgent(dir)
	if err := Write(a); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("file perms too broad: %v", info.Mode().Perm())
	}
}

func TestWrite_AtomicNoPartial(t *testing.T) {
	dir := t.TempDir()
	a := sampleAgent(dir)
	if err := Write(a); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.yaml.tmp")); !os.IsNotExist(err) {
		t.Errorf("tmp file remains: %v", err)
	}
}

func TestWrite_DirCreatedWith0700(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "newdir")
	a := sampleAgent(dir)
	if err := Write(a); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("dir perms=%v want 0700", info.Mode().Perm())
	}
}

func TestWrite_ReplaceExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("p"), 0o600); err != nil {
		t.Fatal(err)
	}
	a1 := sampleAgent(dir)
	a1.Description = "first"
	if err := Write(a1); err != nil {
		t.Fatal(err)
	}
	a2 := sampleAgent(dir)
	a2.Description = "second"
	if err := Write(a2); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Description != "second" {
		t.Errorf("description=%q", loaded.Description)
	}
}
