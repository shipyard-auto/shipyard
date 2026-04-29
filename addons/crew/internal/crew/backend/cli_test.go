package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/conversation"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

type panicDisp struct{}

func (panicDisp) Call(context.Context, string, map[string]any) (tools.Envelope, error) {
	panic("dispatcher must not be called by CLIBackend")
}

func cliAgent(cmd ...string) *crew.Agent {
	return &crew.Agent{
		Name:    "t",
		Backend: crew.Backend{Type: crew.BackendCLI, Command: cmd},
	}
}

func TestCLI_HappyPath(t *testing.T) {
	b := NewCLIBackend()
	out, err := b.Run(context.Background(), RunInput{
		User:  "hello",
		Agent: cliAgent("/bin/sh", "-c", "cat"),
	}, panicDisp{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text != "hello" {
		t.Fatalf("Text=%q", out.Text)
	}
	if out.History.SessionID != "" {
		t.Fatalf("SessionID=%q want empty", out.History.SessionID)
	}
}

func TestCLI_SessionIDExtracted(t *testing.T) {
	b := NewCLIBackend()
	out, err := b.Run(context.Background(), RunInput{
		User:  "hi",
		Agent: cliAgent("/bin/sh", "-c", "cat; echo 'session=abc-123' 1>&2"),
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.History.SessionID != "abc-123" {
		t.Fatalf("SessionID=%q", out.History.SessionID)
	}
}

func TestCLI_PreservesPreviousSessionWhenNoMatch(t *testing.T) {
	b := NewCLIBackend()
	out, err := b.Run(context.Background(), RunInput{
		User:    "hi",
		History: conversation.History{SessionID: "prev"},
		Agent:   cliAgent("/bin/sh", "-c", "cat"),
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.History.SessionID != "prev" {
		t.Fatalf("SessionID=%q want prev", out.History.SessionID)
	}
}

func TestCLI_ResumeFlagAppended(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "shim.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argsFile+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{
		User:    "x",
		History: conversation.History{SessionID: "prev-sid"},
		Agent:   cliAgent(script, "base"),
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	want := []string{"base", "--resume", "prev-sid", "--permission-mode", "bypassPermissions"}
	if len(lines) != len(want) {
		t.Fatalf("argv=%v want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q", i, lines[i], want[i])
		}
	}
}

func TestCLI_EmptyCommand(t *testing.T) {
	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{User: "x", Agent: &crew.Agent{}}, nil)
	if err == nil || !strings.Contains(err.Error(), "empty command") {
		t.Fatalf("got err=%v", err)
	}
	_, err = b.Run(context.Background(), RunInput{User: "x", Agent: nil}, nil)
	if err == nil || !strings.Contains(err.Error(), "empty command") {
		t.Fatalf("nil agent err=%v", err)
	}
}

func TestCLI_NonZeroExit(t *testing.T) {
	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{
		User:  "x",
		Agent: cliAgent("/bin/sh", "-c", "echo boom 1>&2; exit 7"),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "cli run") {
		t.Fatalf("got err=%v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr not surfaced: %v", err)
	}
}

func TestCLI_ContextCanceled(t *testing.T) {
	b := NewCLIBackend()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := b.Run(ctx, RunInput{
		User:  "x",
		Agent: cliAgent("/bin/sh", "-c", "sleep 5"),
	}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error on timeout")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err=%v want canceled", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("err should wrap ctx error, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("process did not die promptly: %v", elapsed)
	}
}

func TestCLI_OversizedStdout(t *testing.T) {
	b := NewCLIBackend()
	// Produce ~5 MB of 'a' bytes; writer must cap at cliMaxStdoutBytes.
	big := cliMaxStdoutBytes + 1024*1024
	out, err := b.Run(context.Background(), RunInput{
		User:  "x",
		Agent: cliAgent("/bin/sh", "-c", "head -c "+itoa(big)+" /dev/zero | tr '\\0' a"),
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Text) != cliMaxStdoutBytes {
		t.Fatalf("text len=%d want exactly %d", len(out.Text), cliMaxStdoutBytes)
	}
}

func TestCLI_DispatcherIgnored(t *testing.T) {
	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{
		User:  "hi",
		Agent: cliAgent("/bin/sh", "-c", "cat"),
	}, panicDisp{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestCLI_RegexOverride(t *testing.T) {
	b := NewCLIBackend().WithSessionRegex(regexp.MustCompile(`trace-id=(\w+)`))
	out, err := b.Run(context.Background(), RunInput{
		User:  "hi",
		Agent: cliAgent("/bin/sh", "-c", "cat; echo 'trace-id=xyz' 1>&2"),
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.History.SessionID != "xyz" {
		t.Fatalf("SessionID=%q", out.History.SessionID)
	}
}

// captureArgvAgent builds an agent whose command is a shim that writes its
// own argv (one per line) to argsFile, then echoes stdin unchanged. Used to
// empirically verify how the CLI backend assembles argv.
func captureArgvAgent(t *testing.T, cmd []string, flag string) (*crew.Agent, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "shim.sh")
	body := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + argsFile + "; done\ncat\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	full := append([]string{script}, cmd...)
	a := &crew.Agent{
		Name: "t",
		Backend: crew.Backend{
			Type:             crew.BackendCLI,
			Command:          full,
			SystemPromptFlag: flag,
		},
	}
	return a, argsFile
}

func readArgvLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

func TestCLI_PromptAppendedWithDefaultFlag(t *testing.T) {
	agent, argsFile := captureArgvAgent(t, []string{"base"}, "")

	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{
		User:   "ping",
		Prompt: "you are an RE tutor for 15-year-olds",
		Agent:  agent,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readArgvLines(t, argsFile)
	want := []string{"base", "--append-system-prompt", "you are an RE tutor for 15-year-olds", "--permission-mode", "bypassPermissions"}
	if len(got) != len(want) {
		t.Fatalf("argv=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestCLI_PromptUsesOverrideFlag(t *testing.T) {
	agent, argsFile := captureArgvAgent(t, []string{"base"}, "-s")

	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{
		User:   "ping",
		Prompt: "custom",
		Agent:  agent,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readArgvLines(t, argsFile)
	want := []string{"base", "-s", "custom", "--permission-mode", "bypassPermissions"}
	if len(got) != len(want) {
		t.Fatalf("argv=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestCLI_NoPromptNoFlagAppended(t *testing.T) {
	// Empty / whitespace-only prompts must leave argv untouched so agents
	// whose behaviour is self-contained in the command still work.
	agent, argsFile := captureArgvAgent(t, []string{"base"}, "")

	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{
		User:   "ping",
		Prompt: "   \n\t  ",
		Agent:  agent,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readArgvLines(t, argsFile)
	want := []string{"base", "--permission-mode", "bypassPermissions"}
	if len(got) != len(want) {
		t.Fatalf("argv=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

// claudePrintShim writes a fake `claude --print --output-format json`
// response to stdout. The shim captures its argv to argsFile so the test
// can assert that --output-format json was appended by the backend. The
// body is emitted verbatim regardless of stdin so happy/error paths are
// interchangeable.
func claudePrintShim(t *testing.T, body string) (*crew.Agent, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "claude.sh")
	// Use printf so bodies with single quotes survive the shell.
	src := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + argsFile + "; done\n" +
		"cat >/dev/null\n" +
		"cat <<'EOF'\n" + body + "\nEOF\n"
	if err := os.WriteFile(script, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &crew.Agent{
		Name:         "t",
		Backend:      crew.Backend{Type: crew.BackendCLI, Command: []string{script}},
		Conversation: crew.Conversation{Mode: crew.ConversationStateful, Key: "{{input.chat_id}}"},
	}
	return a, argsFile
}

func TestCLI_StatefulAddsOutputFormatJSONAndParses(t *testing.T) {
	agent, argsFile := claudePrintShim(t, `{"type":"result","is_error":false,"result":"Oi!","session_id":"abc-123","usage":{"input_tokens":2,"output_tokens":7}}`)

	b := NewCLIBackend()
	out, err := b.Run(context.Background(), RunInput{User: "hi", Agent: agent}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text != "Oi!" {
		t.Fatalf("Text=%q want %q", out.Text, "Oi!")
	}
	if out.History.SessionID != "abc-123" {
		t.Fatalf("SessionID=%q want %q", out.History.SessionID, "abc-123")
	}
	if out.Usage.InputTokens != 2 || out.Usage.OutputTokens != 7 {
		t.Fatalf("Usage=%+v", out.Usage)
	}

	// argv must contain the JSON flag pair when stateful.
	argv := strings.Join(readArgvLines(t, argsFile), " ")
	if !strings.Contains(argv, "--output-format json") {
		t.Fatalf("missing --output-format json: %v", argv)
	}
}

func TestCLI_StatefulIsErrorPropagates(t *testing.T) {
	agent, _ := claudePrintShim(t, `{"type":"result","is_error":true,"result":"boom","session_id":"s1"}`)

	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{User: "hi", Agent: agent}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want error wrapping \"boom\", got %v", err)
	}
}

func TestCLI_StatefulEmptySessionKeepsPrevious(t *testing.T) {
	// Defensive contract: if Claude ever drops session_id for a turn, we
	// keep the previous id so the conversation state is not lost.
	agent, _ := claudePrintShim(t, `{"type":"result","is_error":false,"result":"ok","session_id":""}`)

	b := NewCLIBackend()
	out, err := b.Run(context.Background(), RunInput{
		User:    "hi",
		Agent:   agent,
		History: conversation.History{SessionID: "old-sid"},
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.History.SessionID != "old-sid" {
		t.Fatalf("SessionID=%q want fallback %q", out.History.SessionID, "old-sid")
	}
}

func TestCLI_StatefulBadJSONReturnsError(t *testing.T) {
	agent, _ := claudePrintShim(t, `not json at all`)

	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{User: "hi", Agent: agent}, nil)
	if err == nil || !strings.Contains(err.Error(), "parse json") {
		t.Fatalf("want json parse error, got %v", err)
	}
}

func TestCLI_StatelessDoesNotAddJSONFlag(t *testing.T) {
	// Regression guard: agents without Conversation.Mode stay on the text
	// path. Breaking this would change behaviour for every existing crew.
	agent, argsFile := captureArgvAgent(t, []string{"base"}, "")

	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{User: "hi", Agent: agent}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv := strings.Join(readArgvLines(t, argsFile), " ")
	if strings.Contains(argv, "--output-format") {
		t.Fatalf("stateless must not append --output-format: %v", argv)
	}
}

func TestCLI_MCPFlagsInjectedWhenToolsDeclared(t *testing.T) {
	// When the agent declares tools, the backend must emit the MCP bridge
	// config AND bypass the interactive permission prompt — otherwise
	// `claude --print` refuses tool calls. This test pins the contract.
	agent, argsFile := captureArgvAgent(t, []string{"base"}, "")
	agent.Tools = []crew.Tool{
		{Name: "echo", Protocol: crew.ToolExec, Command: []string{"/bin/true"}},
	}

	b := NewCLIBackend().
		WithSelfPath("/fake/shipyard-crew").
		WithUserHomeDir(func() (string, error) { return t.TempDir(), nil })
	_, err := b.Run(context.Background(), RunInput{
		User:  "ping",
		Agent: agent,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readArgvLines(t, argsFile)
	argv := strings.Join(got, " ")
	if !strings.Contains(argv, "--mcp-config") {
		t.Fatalf("missing --mcp-config: %v", got)
	}
	if !strings.Contains(argv, "--strict-mcp-config") {
		t.Fatalf("missing --strict-mcp-config: %v", got)
	}
	if !strings.Contains(argv, "--permission-mode bypassPermissions") {
		t.Fatalf("missing --permission-mode bypassPermissions: %v", got)
	}
}

func TestCLI_NoMCPFlagsWhenAgentHasNoToolsOrMCP(t *testing.T) {
	// Zero-config agents get --permission-mode bypassPermissions (always) but
	// must NOT receive --mcp-config or --strict-mcp-config.
	agent, argsFile := captureArgvAgent(t, []string{"base"}, "")

	b := NewCLIBackend().WithUserHomeDir(func() (string, error) { return t.TempDir(), nil })
	_, err := b.Run(context.Background(), RunInput{User: "ping", Agent: agent}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readArgvLines(t, argsFile)
	argv := strings.Join(got, " ")
	if strings.Contains(argv, "--mcp-config") || strings.Contains(argv, "--strict-mcp-config") {
		t.Fatalf("MCP flags must not appear when no tools declared, got: %v", got)
	}
}

func TestCLI_PromptPrecedesResumeFlag(t *testing.T) {
	agent, argsFile := captureArgvAgent(t, []string{"base"}, "")

	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{
		User:    "ping",
		Prompt:  "sys",
		History: conversation.History{SessionID: "sid-42"},
		Agent:   agent,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readArgvLines(t, argsFile)
	want := []string{"base", "--append-system-prompt", "sys", "--resume", "sid-42", "--permission-mode", "bypassPermissions"}
	if len(got) != len(want) {
		t.Fatalf("argv=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

// TestCLI_BypassPermissionsAlwaysAppended garante que --permission-mode
// bypassPermissions é anexado ao argv do claude --print mesmo quando o agente
// não declara tools nem mcp_servers (cfgPath == ""). Antes do fix de C-01, o
// flag só vinha dentro do bloco `if cfgPath != ""`, e agentes minimalistas
// rodavam em permission-mode default — gerando falso-success silencioso.
func TestCLI_BypassPermissionsAlwaysAppended(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "shim.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argsFile+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	b := NewCLIBackend()
	_, err := b.Run(context.Background(), RunInput{
		User:  "x",
		Agent: cliAgent(script, "base"), // sem tools, sem mcp_servers — cfgPath == ""
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	// Procuramos o par "--permission-mode" "bypassPermissions" em sequência.
	found := false
	for i := 0; i < len(lines)-1; i++ {
		if lines[i] == "--permission-mode" && lines[i+1] == "bypassPermissions" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected --permission-mode bypassPermissions in argv (minimalist agent), got: %v", lines)
	}

	// Sanity: NÃO deve aparecer --mcp-config/--strict-mcp-config quando cfgPath == "".
	for _, arg := range lines {
		if arg == "--mcp-config" || arg == "--strict-mcp-config" {
			t.Errorf("MCP flags must not be present for an agent without tools/mcp_servers, got %q in %v", arg, lines)
		}
	}
}

func TestLimitedWriter_Behaviour(t *testing.T) {
	var buf bytesWriter
	lw := &limitedWriter{w: &buf, limit: 5}

	n, err := lw.Write([]byte("abc"))
	if err != nil || n != 3 {
		t.Fatalf("write1: n=%d err=%v", n, err)
	}
	n, err = lw.Write([]byte("defgh"))
	if err != nil || n != 5 {
		t.Fatalf("write2: n=%d err=%v", n, err)
	}
	if buf.String() != "abcde" {
		t.Fatalf("buf=%q", buf.String())
	}
	n, err = lw.Write([]byte("ij"))
	if err != nil || n != 2 {
		t.Fatalf("write3 post-cut: n=%d err=%v", n, err)
	}
	if buf.String() != "abcde" {
		t.Fatalf("buf after cut=%q", buf.String())
	}
}

type bytesWriter struct{ b []byte }

func (w *bytesWriter) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
func (w *bytesWriter) String() string { return string(w.b) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
