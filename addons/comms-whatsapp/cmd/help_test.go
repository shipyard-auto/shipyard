package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestOverviewGivesAgentsContext(t *testing.T) {
	setup(t)
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		out, _, err := cli(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		for _, want := range []string{"Concepts", "allowlist", "no_history", "Rules for agents", "help sending", "send ", "watch "} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v: overview missing %q:\n%s", args, want, out)
			}
		}
	}
}

// Every documented command must answer --help with its real flags, and
// every command the dispatcher knows must be documented.
func TestEveryCommandHasHelp(t *testing.T) {
	setup(t)
	for _, c := range cmdDocs {
		out, _, err := cli(t, c.name, "--help")
		if err != nil {
			t.Fatalf("%s --help: %v", c.name, err)
		}
		if !strings.Contains(out, "Usage:") || !strings.Contains(out, c.summary) {
			t.Fatalf("%s --help output:\n%s", c.name, out)
		}
		viaHelp, _, err := cli(t, "help", c.name)
		if err != nil || viaHelp != out {
			t.Fatalf("help %s differs from %s --help (err %v)", c.name, c.name, err)
		}
	}
	// Commands in the dispatcher's switch, read from the source of truth.
	dispatchCmds := regexp.MustCompile(`case "([a-z]+)":`).FindAllStringSubmatch(dispatchSource(t), -1)
	for _, m := range dispatchCmds {
		if m[1] == "help" {
			continue
		}
		if _, ok := findCmd(m[1]); !ok {
			t.Fatalf("command %q is dispatched but undocumented", m[1])
		}
	}
}

func TestCommandHelpListsRealFlags(t *testing.T) {
	setup(t)
	out, _, err := cli(t, "send", "-h")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-idempotency-key", "-dry-run", "-stdin", "Examples:", "help sending",
		`echo "texto longo" | ` + progName() + " send"} {
		if !strings.Contains(out, want) {
			t.Fatalf("send -h missing %q:\n%s", want, out)
		}
	}
	// A value that looks like a flag is a value, not a help request.
	if _, _, err := cli(t, "send", "x", "--text", "-h"); err == nil || strings.Contains(err.Error(), "help shown") {
		t.Fatalf("--text -h treated as help: %v", err)
	}
}

func TestTopicsAndErrors(t *testing.T) {
	setup(t)
	for _, topic := range topics {
		if _, clash := findCmd(topic.name); clash {
			t.Fatalf("topic %q is shadowed by the command of the same name", topic.name)
		}
		out, _, err := cli(t, "help", topic.name)
		if err != nil || !strings.Contains(out, topic.summary) {
			t.Fatalf("help %s: %v\n%s", topic.name, err, out)
		}
	}
	if _, _, err := cli(t, "help", "nope"); err == nil || !strings.Contains(err.Error(), "help' for the list") {
		t.Fatalf("unknown topic: %v", err)
	}
	if _, _, err := cli(t, "chats", "--bogus"); err == nil || !strings.Contains(err.Error(), "chats --help") {
		t.Fatalf("bad flag hint: %v", err)
	}
	if _, _, err := cli(t, "nope"); err == nil || !strings.Contains(err.Error(), "help' for the list") {
		t.Fatalf("unknown command: %v", err)
	}
}

func TestProgNameFollowsWrapper(t *testing.T) {
	setup(t)
	t.Setenv(ProgEnv, "shipyard comms whatsapp")
	out, _, err := cli(t, "help", "watch")
	if err != nil || !strings.Contains(out, "Usage: shipyard comms whatsapp watch <chat>") ||
		!strings.Contains(out, "  shipyard comms whatsapp watch \"Família\" --ttl 7d") {
		t.Fatalf("help with wrapper name:\n%s (err %v)", out, err)
	}
}

// dispatchSource returns main.go, where the dispatcher's switch lives;
// reading it keeps the test honest when a command is added without docs.
func dispatchSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
