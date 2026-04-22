package crew

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeAgent writes agent.yaml (plus an empty prompt.md to be tolerant even
// though list does not read it) into <home>/crew/<name>/.
func writeAgent(t *testing.T, home, name, body string) {
	t.Helper()
	dir := filepath.Join(home, "crew", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write agent.yaml: %v", err)
	}
}

func writePID(t *testing.T, home, name string, pid int) {
	t.Helper()
	dir := filepath.Join(home, "run", "crew")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".pid"), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatalf("write pid: %v", err)
	}
}

func TestRunList_EmptyHome(t *testing.T) {
	home := t.TempDir()
	var stdout bytes.Buffer
	deps := listDeps{Home: home, Stdout: &stdout}
	if err := runList(deps, listFlags{}); err != nil {
		t.Fatalf("runList: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "no crew members") {
		t.Errorf("expected \"no crew members\", got %q", got)
	}
}

func TestRunList_EmptyHomeJSON(t *testing.T) {
	home := t.TempDir()
	var stdout bytes.Buffer
	deps := listDeps{Home: home, Stdout: &stdout}
	if err := runList(deps, listFlags{JSON: true}); err != nil {
		t.Fatalf("runList: %v", err)
	}
	var out []listEntry
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json: %v (body=%q)", err, stdout.String())
	}
	if len(out) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(out))
	}
}

const serviceAgentYAML = `schema_version: "1"
name: chat-bot
description: handles support chat
backend:
  type: cli
  command: ["claude"]
execution:
  mode: service
  pool: cli
conversation:
  mode: stateless
triggers:
  - type: webhook
    route: /chat
`

const onDemandAgentYAML = `schema_version: "1"
name: promo-hunter
description: finds promos
backend:
  type: anthropic_api
  model: claude-opus-4-7
execution:
  mode: on-demand
  pool: cli
conversation:
  mode: stateless
triggers:
  - type: cron
    schedule: "*/5 * * * *"
  - type: webhook
    route: /promo
`

func TestRunList_TwoEntriesAlphabetical(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "promo-hunter", onDemandAgentYAML)
	writeAgent(t, home, "chat-bot", serviceAgentYAML)
	writePID(t, home, "chat-bot", os.Getpid())

	var stdout bytes.Buffer
	deps := listDeps{Home: home, Stdout: &stdout}
	if err := runList(deps, listFlags{}); err != nil {
		t.Fatalf("runList: %v", err)
	}

	out := stdout.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (header + 2), got %d: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "NAME") {
		t.Errorf("missing header, got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "chat-bot") {
		t.Errorf("expected chat-bot first, got %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "promo-hunter") {
		t.Errorf("expected promo-hunter second, got %q", lines[2])
	}
	if !strings.Contains(lines[1], "running") {
		t.Errorf("expected running state for chat-bot, got %q", lines[1])
	}
	if !strings.Contains(lines[2], "cron,webhook") {
		t.Errorf("expected \"cron,webhook\" triggers, got %q", lines[2])
	}
}

func TestRunList_ServiceWithDeadPID(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "chat-bot", serviceAgentYAML)
	writePID(t, home, "chat-bot", 123456)

	var stdout bytes.Buffer
	deps := listDeps{
		Home:    home,
		Stdout:  &stdout,
		IsAlive: func(pid int) bool { return false },
	}
	if err := runList(deps, listFlags{}); err != nil {
		t.Fatalf("runList: %v", err)
	}
	if !strings.Contains(stdout.String(), "stopped") {
		t.Errorf("expected stopped state, got %q", stdout.String())
	}
}

func TestRunList_ServiceNoPIDFile(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "chat-bot", serviceAgentYAML)

	var stdout bytes.Buffer
	deps := listDeps{Home: home, Stdout: &stdout}
	if err := runList(deps, listFlags{}); err != nil {
		t.Fatalf("runList: %v", err)
	}
	if !strings.Contains(stdout.String(), "stopped") {
		t.Errorf("expected stopped state, got %q", stdout.String())
	}
}

func TestRunList_OnDemandStateDash(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "promo-hunter", onDemandAgentYAML)

	var stdout bytes.Buffer
	deps := listDeps{Home: home, Stdout: &stdout}
	if err := runList(deps, listFlags{}); err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected header + 1 row, got %d", len(lines))
	}
	fields := strings.Fields(lines[1])
	// Columns: NAME BACKEND MODE POOL TRIGGERS STATE
	if fields[len(fields)-1] != "-" {
		t.Errorf("expected state \"-\" for on-demand, got %q", fields[len(fields)-1])
	}
}

func TestRunList_JSON(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "chat-bot", serviceAgentYAML)
	writeAgent(t, home, "promo-hunter", onDemandAgentYAML)
	writePID(t, home, "chat-bot", os.Getpid())

	var stdout bytes.Buffer
	deps := listDeps{Home: home, Stdout: &stdout}
	if err := runList(deps, listFlags{JSON: true}); err != nil {
		t.Fatalf("runList: %v", err)
	}

	var out []listEntry
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json: %v (body=%q)", err, stdout.String())
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(out))
	}
	if out[0].Name != "chat-bot" || out[0].State != "running" {
		t.Errorf("unexpected first entry: %+v", out[0])
	}
	if out[0].Backend != "cli" || out[0].Mode != "service" || out[0].Pool != "cli" {
		t.Errorf("unexpected first entry fields: %+v", out[0])
	}
	if len(out[0].Triggers) != 1 || out[0].Triggers[0] != "webhook" {
		t.Errorf("unexpected first entry triggers: %+v", out[0].Triggers)
	}
	if out[1].Name != "promo-hunter" || out[1].State != "-" {
		t.Errorf("unexpected second entry: %+v", out[1])
	}
	if len(out[1].Triggers) != 2 || out[1].Triggers[0] != "cron" || out[1].Triggers[1] != "webhook" {
		t.Errorf("unexpected second entry triggers: %+v", out[1].Triggers)
	}
}

func TestRunList_LongIncludesDescription(t *testing.T) {
	home := t.TempDir()
	writeAgent(t, home, "chat-bot", serviceAgentYAML)
	writePID(t, home, "chat-bot", os.Getpid())

	var stdout bytes.Buffer
	deps := listDeps{Home: home, Stdout: &stdout}
	if err := runList(deps, listFlags{Long: true}); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "DESCRIPTION") {
		t.Errorf("expected DESCRIPTION column, got %q", out)
	}
	if !strings.Contains(out, "handles support chat") {
		t.Errorf("expected description text, got %q", out)
	}
}

func TestRunList_IgnoresDirWithoutAgentYaml(t *testing.T) {
	home := t.TempDir()
	// A directory that looks like a crew member but has no agent.yaml.
	if err := os.MkdirAll(filepath.Join(home, "crew", "ghost"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeAgent(t, home, "chat-bot", serviceAgentYAML)
	writePID(t, home, "chat-bot", os.Getpid())

	var stdout bytes.Buffer
	deps := listDeps{Home: home, Stdout: &stdout}
	if err := runList(deps, listFlags{}); err != nil {
		t.Fatalf("runList: %v", err)
	}
	if strings.Contains(stdout.String(), "ghost") {
		t.Errorf("expected ghost to be skipped, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "chat-bot") {
		t.Errorf("expected chat-bot in output, got %q", stdout.String())
	}
}

func TestRunList_IgnoresFilesAtCrewRoot(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "crew"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "crew", "README"), []byte("ignore me"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	var stdout bytes.Buffer
	deps := listDeps{Home: home, Stdout: &stdout}
	if err := runList(deps, listFlags{}); err != nil {
		t.Fatalf("runList: %v", err)
	}
	if !strings.Contains(stdout.String(), "no crew members") {
		t.Errorf("expected \"no crew members\", got %q", stdout.String())
	}
}

func TestRunList_SkipsInvalidYAMLWithVerboseWarning(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "crew", "broken")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(":::not yaml"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stdout, stderr bytes.Buffer
	deps := listDeps{Home: home, Stdout: &stdout, Stderr: &stderr, Verbose: true}
	if err := runList(deps, listFlags{Verbose: true}); err != nil {
		t.Fatalf("runList: %v", err)
	}
	if !strings.Contains(stdout.String(), "no crew members") {
		t.Errorf("expected empty list, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "skip broken") {
		t.Errorf("expected verbose warning on stderr, got %q", stderr.String())
	}
}

func TestResolveState_TableDriven(t *testing.T) {
	home := t.TempDir()

	cases := []struct {
		name     string
		mode     string
		pid      string
		isAlive  func(int) bool
		expected string
	}{
		{"on-demand", ExecutionModeOnDemand, "", nil, "-"},
		{"empty mode", "", "", nil, "-"},
		{"service no pid file", ExecutionModeService, "", nil, "stopped"},
		{"service live pid", ExecutionModeService, "42", func(int) bool { return true }, "running"},
		{"service dead pid", ExecutionModeService, "42", func(int) bool { return false }, "stopped"},
		{"service corrupt pid", ExecutionModeService, "not-a-number", func(int) bool { return true }, "stopped"},
		{"service zero pid", ExecutionModeService, "0", func(int) bool { return true }, "stopped"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "agent-" + tc.name
			if tc.pid != "" {
				dir := filepath.Join(home, "run", "crew")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, name+".pid"), []byte(tc.pid), 0o600); err != nil {
					t.Fatalf("write pid: %v", err)
				}
			}
			isAlive := tc.isAlive
			if isAlive == nil {
				isAlive = func(int) bool { return true }
			}
			got := resolveState(home, name, tc.mode, isAlive)
			if got != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestPIDAlive_OwnPID(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Errorf("expected own PID to be alive")
	}
}

func TestPIDAlive_BogusPID(t *testing.T) {
	// PID 0x7fffffff is effectively guaranteed to not exist on any real system.
	if pidAlive(0x7fffffff) {
		t.Errorf("expected bogus PID to be dead")
	}
}

func TestNewListCmd_FlagsWired(t *testing.T) {
	cmd := newListCmd()
	if cmd.Flags().Lookup("json") == nil {
		t.Errorf("missing --json flag")
	}
	if cmd.Flags().Lookup("long") == nil {
		t.Errorf("missing --long flag")
	}
	if cmd.Flags().Lookup("verbose") == nil {
		t.Errorf("missing --verbose flag")
	}
}
