package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

const helperEnvVar = "CREW_EXEC_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnvVar); mode != "" {
		runHelper(mode)
		return
	}
	os.Exit(m.Run())
}

func runHelper(mode string) {
	switch mode {
	case "ok":
		io.Copy(io.Discard, os.Stdin)
		fmt.Print(`{"ok":true,"data":{"hello":"world"}}`)
	case "fail":
		io.Copy(io.Discard, os.Stdin)
		fmt.Fprint(os.Stderr, "boom\n")
		os.Exit(2)
	case "big":
		io.Copy(io.Discard, os.Stdin)
		chunk := bytes.Repeat([]byte("x"), 64*1024)
		total := 5 * 1024 * 1024
		for written := 0; written < total; written += len(chunk) {
			os.Stdout.Write(chunk)
		}
	case "slow":
		io.Copy(io.Discard, os.Stdin)
		time.Sleep(5 * time.Second)
		fmt.Print(`{"ok":true}`)
	case "invalid":
		io.Copy(io.Discard, os.Stdin)
		fmt.Print("not json at all")
	case "echo-stdin":
		raw, _ := io.ReadAll(os.Stdin)
		fmt.Printf(`{"ok":true,"data":%s}`, raw)
	case "echo-argv":
		io.Copy(io.Discard, os.Stdin)
		argv, _ := json.Marshal(os.Args[1:])
		fmt.Printf(`{"ok":true,"data":%s}`, argv)
	case "echo-env":
		io.Copy(io.Discard, os.Stdin)
		payload := map[string]string{
			"FOO":  os.Getenv("FOO"),
			"PATH": os.Getenv("PATH"),
		}
		b, _ := json.Marshal(payload)
		fmt.Printf(`{"ok":true,"data":%s}`, b)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
		os.Exit(99)
	}
	os.Exit(0)
}

func execTool(mode string, extraArgs ...string) crew.Tool {
	cmd := append([]string{os.Args[0]}, extraArgs...)
	return crew.Tool{
		Name:     "helper",
		Protocol: crew.ToolExec,
		Command:  cmd,
	}
}

func helperEnv(mode string, extra map[string]string) map[string]string {
	env := map[string]string{helperEnvVar: mode}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func TestExecDriver_HappyPath(t *testing.T) {
	d := NewExecDriver()
	env, err := d.Execute(context.Background(), execTool("ok"), map[string]any{"a": 1}, DriverContext{
		AgentName: "jarvis",
		AgentDir:  t.TempDir(),
		Env:       helperEnv("ok", nil),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !env.Ok {
		t.Fatalf("want Ok=true, got %+v", env)
	}
	if string(env.Data) != `{"hello":"world"}` {
		t.Fatalf("data = %s", env.Data)
	}
}

func TestExecDriver_ExitNonZero(t *testing.T) {
	d := NewExecDriver()
	env, err := d.Execute(context.Background(), execTool("fail"), nil, DriverContext{
		Env: helperEnv("fail", nil),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want Ok=false, got %+v", env)
	}
	if !strings.HasPrefix(env.Error, "tool crashed: exit=2") {
		t.Fatalf("error = %q", env.Error)
	}
	var details map[string]any
	if err := json.Unmarshal(env.Details, &details); err != nil {
		t.Fatalf("details decode: %v", err)
	}
	stderr, _ := details["stderr"].(string)
	if !strings.Contains(stderr, "boom") {
		t.Fatalf("stderr should contain boom, got %q", stderr)
	}
}

func TestExecDriver_StdoutOverflow(t *testing.T) {
	d := NewExecDriver()
	env, err := d.Execute(context.Background(), execTool("big"), nil, DriverContext{
		Env: helperEnv("big", nil),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want Ok=false, got %+v", env)
	}
	if !strings.Contains(env.Error, "exceeded") {
		t.Fatalf("error = %q", env.Error)
	}
}

func TestExecDriver_Timeout(t *testing.T) {
	d := &ExecDriver{Now: time.Now, Timeout: 200 * time.Millisecond}
	start := time.Now()
	env, err := d.Execute(context.Background(), execTool("slow"), nil, DriverContext{
		Env: helperEnv("slow", nil),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout not enforced")
	}
	if env.Ok {
		t.Fatalf("want Ok=false, got %+v", env)
	}
	if env.Error != "exec: tool timeout" {
		t.Fatalf("error = %q", env.Error)
	}
	var details map[string]any
	if err := json.Unmarshal(env.Details, &details); err != nil {
		t.Fatalf("details decode: %v", err)
	}
	if details["timeout_seconds"] != 0.2 {
		t.Fatalf("timeout_seconds = %v", details["timeout_seconds"])
	}
}

func TestExecDriver_InvalidEnvelope(t *testing.T) {
	d := NewExecDriver()
	env, err := d.Execute(context.Background(), execTool("invalid"), nil, DriverContext{
		Env: helperEnv("invalid", nil),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want Ok=false")
	}
	if !strings.Contains(env.Error, "invalid envelope") {
		t.Fatalf("error = %q", env.Error)
	}
	var details map[string]any
	if err := json.Unmarshal(env.Details, &details); err != nil {
		t.Fatalf("details decode: %v", err)
	}
	sample, _ := details["stdout_sample"].(string)
	if !strings.Contains(sample, "not json") {
		t.Fatalf("stdout_sample = %q", sample)
	}
}

func TestExecDriver_StdinCarriesInput(t *testing.T) {
	d := NewExecDriver()
	input := map[string]any{"a": float64(1)}
	env, err := d.Execute(context.Background(), execTool("echo-stdin"), input, DriverContext{
		Env: helperEnv("echo-stdin", nil),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !env.Ok {
		t.Fatalf("want Ok=true, got %+v", env)
	}
	var got map[string]any
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if got["a"].(float64) != 1 {
		t.Fatalf("stdin lost: %v", got)
	}
}

func TestExecDriver_ArgvRendering(t *testing.T) {
	d := NewExecDriver()
	tool := crew.Tool{
		Name:     "helper",
		Protocol: crew.ToolExec,
		Command:  []string{"{{env.SELF}}", "{{input.name}}"},
	}
	env, err := d.Execute(context.Background(), tool, map[string]any{"name": "alice"}, DriverContext{
		Env: helperEnv("echo-argv", map[string]string{"SELF": os.Args[0]}),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !env.Ok {
		t.Fatalf("want Ok=true, got %+v", env)
	}
	var argv []string
	if err := json.Unmarshal(env.Data, &argv); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(argv) != 1 || argv[0] != "alice" {
		t.Fatalf("argv = %v", argv)
	}
}

func TestExecDriver_WrongProtocol(t *testing.T) {
	d := NewExecDriver()
	tool := crew.Tool{Name: "x", Protocol: crew.ToolHTTP}
	_, err := d.Execute(context.Background(), tool, nil, DriverContext{})
	if err == nil {
		t.Fatal("expected Go error for wrong protocol")
	}
	if !strings.Contains(err.Error(), "wrong protocol") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecDriver_EmptyCommand(t *testing.T) {
	d := NewExecDriver()
	tool := crew.Tool{Name: "x", Protocol: crew.ToolExec}
	_, err := d.Execute(context.Background(), tool, nil, DriverContext{})
	if err == nil {
		t.Fatal("expected Go error for empty command")
	}
	if !strings.Contains(err.Error(), "empty command") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecDriver_ParentCanceled(t *testing.T) {
	d := NewExecDriver()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env, err := d.Execute(ctx, execTool("ok"), nil, DriverContext{
		Env: helperEnv("ok", nil),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want Ok=false, got %+v", env)
	}
	if env.Error != "exec: canceled" {
		t.Fatalf("error = %q", env.Error)
	}
}

func TestExecDriver_RenderFailure(t *testing.T) {
	d := NewExecDriver()
	tool := crew.Tool{
		Name:     "x",
		Protocol: crew.ToolExec,
		Command:  []string{"{{input.missing}}"},
	}
	env, err := d.Execute(context.Background(), tool, map[string]any{}, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want Ok=false")
	}
	if !strings.Contains(env.Error, "render command failed") {
		t.Fatalf("error = %q", env.Error)
	}
}

func TestExecDriver_EnvOverridesBase(t *testing.T) {
	d := NewExecDriver()
	os.Setenv("FOO", "from-process")
	defer os.Unsetenv("FOO")
	env, err := d.Execute(context.Background(), execTool("echo-env"), nil, DriverContext{
		Env: helperEnv("echo-env", map[string]string{"FOO": "from-driver"}),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !env.Ok {
		t.Fatalf("want Ok=true, got %+v", env)
	}
	var got map[string]string
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["FOO"] != "from-driver" {
		t.Fatalf("FOO = %q, want driver override", got["FOO"])
	}
	if got["PATH"] == "" {
		t.Fatal("PATH not propagated from os.Environ")
	}
}

func TestLimitedWriter_DoesNotBlock(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{W: &buf, N: 4}
	n, err := lw.Write([]byte("abcdefgh"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != 8 {
		t.Fatalf("n = %d, want 8", n)
	}
	if !lw.Overflowed {
		t.Fatal("expected Overflowed=true")
	}
	if buf.String() != "abcd" {
		t.Fatalf("buf = %q", buf.String())
	}
	n, err = lw.Write([]byte("more"))
	if err != nil || n != 4 || !lw.Overflowed {
		t.Fatalf("second write: n=%d err=%v", n, err)
	}
}
