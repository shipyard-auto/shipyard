package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/template"
)

// HTTPTimeout is the hard cap applied to a single http tool invocation when
// the caller context has no deadline of its own. Per-tool configuration is
// tracked in the roadmap (1.3).
const (
	HTTPTimeout          = 60 * time.Second
	maxHTTPResponseBytes = 4 * 1024 * 1024
)

// HTTPDriver invokes tools whose protocol is "http". It uses net/http
// directly — never shells out to curl/wget — and is safe for concurrent use
// by independent goroutines.
type HTTPDriver struct {
	Client  *http.Client
	Timeout time.Duration
}

// NewHTTPDriver returns a driver with production defaults.
func NewHTTPDriver() *HTTPDriver {
	return &HTTPDriver{Client: &http.Client{}, Timeout: HTTPTimeout}
}

// Execute implements Driver. It renders URL/headers/body against the template
// engine, issues a single HTTP request, reads up to maxHTTPResponseBytes and
// translates the result into an Envelope. Network errors, timeouts, oversized
// responses and non-2xx status codes all become Failure envelopes; the Go
// error return is reserved for contract violations (wrong protocol / empty
// url) that indicate a programming bug rather than a tool runtime issue.
func (d *HTTPDriver) Execute(ctx context.Context, tool crew.Tool, input map[string]any, dc DriverContext) (Envelope, error) {
	if tool.Protocol != crew.ToolHTTP {
		return Envelope{}, fmt.Errorf("http driver: wrong protocol %q", tool.Protocol)
	}
	if strings.TrimSpace(tool.URL) == "" {
		return Envelope{}, errors.New("http driver: empty url")
	}

	tplCtx := template.Context{
		Input: input,
		Env:   dc.Env,
		Agent: map[string]string{
			"name": dc.AgentName,
			"dir":  dc.AgentDir,
		},
	}

	renderedURL, err := template.Render(tool.URL, tplCtx)
	if err != nil {
		return Failure("http: render url failed: "+err.Error(), nil), nil
	}

	renderedHeaders, err := template.RenderMap(tool.Headers, tplCtx)
	if err != nil {
		return Failure("http: render headers failed: "+err.Error(), nil), nil
	}

	var bodyReader io.Reader
	if tool.Body != "" {
		rb, err := template.Render(tool.Body, tplCtx)
		if err != nil {
			return Failure("http: render body failed: "+err.Error(), nil), nil
		}
		bodyReader = strings.NewReader(rb)
	}

	method := tool.Method
	if method == "" {
		method = http.MethodGet
	}

	timeout := d.Timeout
	if timeout == 0 {
		timeout = HTTPTimeout
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, method, renderedURL, bodyReader)
	if err != nil {
		return Failure("http: build request: "+err.Error(), map[string]any{"url": renderedURL}), nil
	}
	for k, v := range renderedHeaders {
		req.Header.Set(k, v)
	}

	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Failure("http timeout", map[string]any{"url": renderedURL}), nil
		}
		return Failure(fmt.Sprintf("http error: %v", err), map[string]any{"url": renderedURL}), nil
	}
	defer resp.Body.Close()

	buf := &bytes.Buffer{}
	n, readErr := io.Copy(buf, io.LimitReader(resp.Body, maxHTTPResponseBytes+1))
	if readErr != nil {
		if ctx.Err() != nil {
			return Failure("http timeout", map[string]any{"url": renderedURL}), nil
		}
		return Failure("http: read body: "+readErr.Error(), map[string]any{"url": renderedURL}), nil
	}
	if n > maxHTTPResponseBytes {
		return Failure("http response oversized", map[string]any{"limit_bytes": maxHTTPResponseBytes}), nil
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		env, perr := Parse(buf.Bytes())
		if perr != nil {
			return Failure("invalid tool output", map[string]any{
				"raw":         truncateStr(buf.String(), 1024),
				"parse_error": perr.Error(),
			}), nil
		}
		return env, nil
	}
	return Failure(fmt.Sprintf("http %d", resp.StatusCode), map[string]any{
		"body": truncateStr(buf.String(), 1024),
	}), nil
}
