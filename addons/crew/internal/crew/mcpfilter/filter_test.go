package mcpfilter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// fakeUpstream builds a tiny stdio MCP server fixture written as a python
// script. It echoes a pre-baked tools/list response, accepts tools/call
// frames and answers with the requested name, and exits when stdin closes.
// Returns an UpstreamConfig that runs the script through python3.
//
// The script is passed to the interpreter as an argument and never exec'd
// itself: exec'ing a freshly written file races with forks from parallel
// tests, which can inherit the write fd and make execve fail with ETXTBSY
// (golang/go#22315).
func fakeUpstream(t *testing.T) UpstreamConfig {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "upstream.py")
	// Fake upstream: parses the request id (assumes simple numeric form) and
	// echoes it back so the proxy can correlate responses to requests.
	body := `import json, sys
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
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return UpstreamConfig{Type: "stdio", Command: "python3", Args: []string{script}}
}

func TestProxy_ToolsListFilteredAndCallEnforced(t *testing.T) {
	t.Parallel()

	upstream := fakeUpstream(t)

	in, out := setupPipes(t)
	defer out.Close()

	p := Proxy{
		Allowed:  []string{"safe"},
		Upstream: upstream,
		In:       in.reader,
		Out:      out,
		Err:      &bytes.Buffer{},
	}

	done := runProxyInBackground(t, p, in, out)

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
	waitProxy(t, done)
}

func TestProxy_InitializePassthrough(t *testing.T) {
	t.Parallel()

	upstream := fakeUpstream(t)

	in, out := setupPipes(t)
	defer out.Close()

	p := Proxy{
		Allowed:  []string{"safe"},
		Upstream: upstream,
		In:       in.reader,
		Out:      out,
		Err:      &bytes.Buffer{},
	}
	done := runProxyInBackground(t, p, in, out)

	in.send(t, `{"jsonrpc":"2.0","id":99,"method":"initialize","params":{}}`)
	resp := out.readFrame(t)
	if !strings.Contains(resp, `"protocolVersion"`) {
		t.Fatalf("initialize should pass through verbatim, got: %s", resp)
	}

	in.close()
	waitProxy(t, done)
}

func TestProxy_WildcardDisablesFilter(t *testing.T) {
	t.Parallel()

	upstream := fakeUpstream(t)

	in, out := setupPipes(t)
	defer out.Close()

	p := Proxy{
		Allowed:  []string{"*"},
		Upstream: upstream,
		In:       in.reader,
		Out:      out,
		Err:      &bytes.Buffer{},
	}
	done := runProxyInBackground(t, p, in, out)

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
	waitProxy(t, done)
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
	errCh := make(chan error, 1)
	go func() {
		_, err := io.WriteString(p.writer, frame+"\n")
		errCh <- err
	}()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	case <-time.After(ioTimeout):
		t.Fatalf("send: proxy did not consume %q within %s", frame, ioTimeout)
	}
}

func (p *pipeReader) close() { _ = p.writer.Close() }

type framedWriter struct {
	pw *io.PipeWriter
	pr *io.PipeReader
}

func (f *framedWriter) Write(p []byte) (int, error) {
	return f.pw.Write(p)
}

func (f *framedWriter) Close() error {
	return f.pw.Close()
}

// ioTimeout bounds every pipe interaction in these tests, so a proxy that
// stops answering fails fast with a message instead of hanging until the
// package timeout.
const ioTimeout = 10 * time.Second

func (f *framedWriter) readFrame(t *testing.T) string {
	t.Helper()
	type result struct {
		frame string
		err   error
	}
	resCh := make(chan result, 1)
	go func() {
		buf := make([]byte, 0, 256)
		one := make([]byte, 1)
		for {
			n, err := f.pr.Read(one)
			if err != nil {
				resCh <- result{string(buf), err}
				return
			}
			if n == 0 {
				continue
			}
			if one[0] == '\n' {
				resCh <- result{string(buf), nil}
				return
			}
			buf = append(buf, one[0])
		}
	}()
	select {
	case res := <-resCh:
		if res.err != nil {
			if errors.Is(res.err, io.EOF) {
				t.Fatalf("readFrame: EOF, partial=%q", res.frame)
			}
			t.Fatalf("readFrame: %v (partial=%q)", res.err, res.frame)
		}
		return res.frame
	case <-time.After(ioTimeout):
		t.Fatalf("readFrame: no frame from proxy within %s", ioTimeout)
		return ""
	}
}

func setupPipes(t *testing.T) (*pipeReader, *framedWriter) {
	t.Helper()
	inPr, inPw := io.Pipe()
	outPr, outPw := io.Pipe()
	t.Cleanup(func() {
		// Unblock any reader goroutine left behind by a failed test.
		_ = outPr.Close()
		_ = inPr.Close()
	})
	return &pipeReader{reader: inPr, writer: inPw}, &framedWriter{pw: outPw, pr: outPr}
}

// runProxyInBackground runs p and, once Run returns, closes both test pipes
// with its result, so a proxy that exits early (e.g. the upstream failed to
// start) surfaces its error in send/readFrame instead of leaving them blocked.
func runProxyInBackground(t *testing.T, p Proxy, in *pipeReader, out *framedWriter) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		err := p.Run(context.Background())
		closeErr := fmt.Errorf("proxy exited: %v (stderr: %s)", err, stderrOf(p))
		_ = in.reader.CloseWithError(closeErr)
		_ = out.pw.CloseWithError(closeErr)
		done <- err
	}()
	return done
}

func stderrOf(p Proxy) string {
	if b, ok := p.Err.(*bytes.Buffer); ok {
		return strings.TrimSpace(b.String())
	}
	return ""
}

func waitProxy(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("proxy: %v", err)
		}
	case <-time.After(ioTimeout):
		t.Fatalf("proxy did not exit within %s after stdin closed", ioTimeout)
	}
}
