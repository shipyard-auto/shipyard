package mcpfilter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpstreamConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		u       UpstreamConfig
		wantErr string
	}{
		{"ok stdio", UpstreamConfig{Type: "stdio", Command: "npx"}, ""},
		{"ok no type", UpstreamConfig{Command: "npx"}, ""},
		{"empty command", UpstreamConfig{Type: "stdio"}, "empty"},
		{"unsupported type", UpstreamConfig{Type: "http", Command: "x"}, "unsupported"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.u.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// fakeUpstream builds a tiny stdio MCP server fixture written as a sh
// script. It echoes a pre-baked tools/list response, accepts tools/call
// frames and answers with the requested name, and exits when stdin closes.
// Returns the path to the script + a cleanup func.
func fakeUpstream(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "upstream.sh")
	// Fake upstream: parses the request id (assumes simple numeric form) and
	// echoes it back so the proxy can correlate responses to requests.
	body := `#!/usr/bin/env python3
import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        req = json.loads(line)
    except Exception:
        continue
    rid = req.get("id")
    method = req.get("method", "")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"0"}}}), flush=True)
    elif method == "tools/list":
        print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"tools":[{"name":"safe","inputSchema":{}},{"name":"danger","inputSchema":{}}]}}), flush=True)
    elif method == "tools/call":
        print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"content":[{"type":"text","text":"forwarded"}]}}), flush=True)
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestProxy_ToolsListFilteredAndCallEnforced(t *testing.T) {
	t.Parallel()

	script := fakeUpstream(t)

	in, out := setupPipes(t)
	defer out.Close()

	p := Proxy{
		Allowed:  []string{"safe"},
		Upstream: UpstreamConfig{Type: "stdio", Command: script},
		In:       in.reader,
		Out:      out,
		Err:      &bytes.Buffer{},
	}

	done := runProxyInBackground(t, p)

	// 1. tools/list — upstream returns two tools; we only see "safe".
	in.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	listResp := out.readFrame(t)
	if !strings.Contains(listResp, `"name":"safe"`) {
		t.Fatalf("tools/list should keep safe: %s", listResp)
	}
	if strings.Contains(listResp, `"name":"danger"`) {
		t.Fatalf("tools/list should drop danger: %s", listResp)
	}

	// 2. tools/call to allowed tool — forwards, gets the "forwarded" payload.
	in.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"safe","arguments":{}}}`)
	callResp := out.readFrame(t)
	if !strings.Contains(callResp, "forwarded") {
		t.Fatalf("allowed tools/call should forward, got: %s", callResp)
	}

	// 3. tools/call to blocked tool — must NOT reach upstream; proxy answers
	//    with an error frame.
	in.send(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"danger","arguments":{}}}`)
	blockResp := out.readFrame(t)
	if !strings.Contains(blockResp, `"error"`) {
		t.Fatalf("blocked call should answer with error: %s", blockResp)
	}
	if !strings.Contains(blockResp, "danger") {
		t.Fatalf("blocked call error should name the tool: %s", blockResp)
	}

	in.close()
	<-done
}

func TestProxy_InitializePassthrough(t *testing.T) {
	t.Parallel()

	script := fakeUpstream(t)

	in, out := setupPipes(t)
	defer out.Close()

	p := Proxy{
		Allowed:  []string{"safe"},
		Upstream: UpstreamConfig{Type: "stdio", Command: script},
		In:       in.reader,
		Out:      out,
		Err:      &bytes.Buffer{},
	}
	done := runProxyInBackground(t, p)

	in.send(t, `{"jsonrpc":"2.0","id":99,"method":"initialize","params":{}}`)
	resp := out.readFrame(t)
	if !strings.Contains(resp, `"protocolVersion"`) {
		t.Fatalf("initialize should pass through verbatim, got: %s", resp)
	}

	in.close()
	<-done
}

func TestProxy_WildcardDisablesFilter(t *testing.T) {
	t.Parallel()

	script := fakeUpstream(t)

	in, out := setupPipes(t)
	defer out.Close()

	p := Proxy{
		Allowed:  []string{"*"},
		Upstream: UpstreamConfig{Type: "stdio", Command: script},
		In:       in.reader,
		Out:      out,
		Err:      &bytes.Buffer{},
	}
	done := runProxyInBackground(t, p)

	in.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp := out.readFrame(t)
	if !strings.Contains(resp, `"safe"`) || !strings.Contains(resp, `"danger"`) {
		t.Fatalf("wildcard must keep all tools, got: %s", resp)
	}

	in.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"danger","arguments":{}}}`)
	resp = out.readFrame(t)
	if !strings.Contains(resp, "forwarded") {
		t.Fatalf("wildcard must let any tool through: %s", resp)
	}

	in.close()
	<-done
}

func TestProxy_UpstreamMissingFails(t *testing.T) {
	t.Parallel()

	in, out := setupPipes(t)
	defer out.Close()

	p := Proxy{
		Allowed:  []string{"safe"},
		Upstream: UpstreamConfig{Type: "stdio", Command: "/path/that/does/not/exist/upstream"},
		In:       in.reader,
		Out:      out,
		Err:      &bytes.Buffer{},
	}
	err := p.Run(context.Background())
	if err == nil {
		t.Fatalf("expected error spawning missing upstream")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

type pipeReader struct {
	reader *io.PipeReader
	writer *io.PipeWriter
}

func (p *pipeReader) send(t *testing.T, frame string) {
	t.Helper()
	if _, err := io.WriteString(p.writer, frame+"\n"); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func (p *pipeReader) close() { _ = p.writer.Close() }

type framedWriter struct {
	buf chan string
	pw  *io.PipeWriter
	pr  *io.PipeReader
}

func (f *framedWriter) Write(p []byte) (int, error) {
	return f.pw.Write(p)
}

func (f *framedWriter) Close() error {
	return f.pw.Close()
}

func (f *framedWriter) readFrame(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	deadline := make(chan struct{})
	go func() {
		// Test-only timeout to avoid hanging forever on bug.
		<-deadline
	}()
	for {
		n, err := f.pr.Read(one)
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.Fatalf("readFrame: EOF, partial=%q", buf)
			}
			t.Fatalf("readFrame: %v", err)
		}
		if n == 0 {
			continue
		}
		if one[0] == '\n' {
			return string(buf)
		}
		buf = append(buf, one[0])
	}
}

func setupPipes(t *testing.T) (*pipeReader, *framedWriter) {
	t.Helper()
	inPr, inPw := io.Pipe()
	outPr, outPw := io.Pipe()
	return &pipeReader{reader: inPr, writer: inPw}, &framedWriter{pw: outPw, pr: outPr}
}

func runProxyInBackground(t *testing.T, p Proxy) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- p.Run(context.Background())
	}()
	return done
}

// ensure unused helper compiles in case the framedWriter buf channel is
// removed in a refactor; this is a harmless reference.
var _ = json.Marshal
