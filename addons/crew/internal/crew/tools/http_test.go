package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

func httpTool(server, method, body string, headers map[string]string) crew.Tool {
	return crew.Tool{
		Name:     "ping",
		Protocol: crew.ToolHTTP,
		Method:   method,
		URL:      server,
		Headers:  headers,
		Body:     body,
	}
}

func TestHTTPDriver_WrongProtocol(t *testing.T) {
	d := NewHTTPDriver()
	tool := crew.Tool{Name: "x", Protocol: crew.ToolExec, URL: "http://x"}
	_, err := d.Execute(context.Background(), tool, nil, DriverContext{})
	if err == nil {
		t.Fatalf("want error for wrong protocol")
	}
}

func TestHTTPDriver_EmptyURL(t *testing.T) {
	d := NewHTTPDriver()
	tool := crew.Tool{Name: "x", Protocol: crew.ToolHTTP, Method: "GET", URL: ""}
	_, err := d.Execute(context.Background(), tool, nil, DriverContext{})
	if err == nil {
		t.Fatalf("want error for empty url")
	}
}

func TestHTTPDriver_Success200Envelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"ok":true,"data":{"x":1}}`)
	}))
	defer srv.Close()

	d := NewHTTPDriver()
	env, err := d.Execute(context.Background(), httpTool(srv.URL, "GET", "", nil), nil, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !env.Ok {
		t.Fatalf("want ok=true, got %+v", env)
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data decode: %v", err)
	}
	if data["x"].(float64) != 1 {
		t.Fatalf("data.x = %v", data["x"])
	}
}

func TestHTTPDriver_Success200NonEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"foo":"bar"}`)
	}))
	defer srv.Close()

	d := NewHTTPDriver()
	env, err := d.Execute(context.Background(), httpTool(srv.URL, "GET", "", nil), nil, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want ok=false (body has no 'ok' field), got %+v", env)
	}
	if env.Error != "invalid tool output" {
		t.Fatalf("error = %q", env.Error)
	}
	var details map[string]any
	if err := json.Unmarshal(env.Details, &details); err != nil {
		t.Fatalf("details decode: %v", err)
	}
	if raw, _ := details["raw"].(string); !strings.Contains(raw, `"foo"`) {
		t.Fatalf("details.raw = %q", raw)
	}
}

func TestHTTPDriver_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "boom")
	}))
	defer srv.Close()

	d := NewHTTPDriver()
	env, err := d.Execute(context.Background(), httpTool(srv.URL, "GET", "", nil), nil, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want ok=false, got %+v", env)
	}
	if env.Error != "http 500" {
		t.Fatalf("error = %q", env.Error)
	}
	var details map[string]any
	if err := json.Unmarshal(env.Details, &details); err != nil {
		t.Fatalf("details decode: %v", err)
	}
	if details["body"] != "boom" {
		t.Fatalf("details.body = %v", details["body"])
	}
}

func TestHTTPDriver_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	d := NewHTTPDriver()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	env, err := d.Execute(ctx, httpTool(srv.URL, "GET", "", nil), nil, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want ok=false, got %+v", env)
	}
	if env.Error != "http timeout" {
		t.Fatalf("error = %q", env.Error)
	}
	var details map[string]any
	if err := json.Unmarshal(env.Details, &details); err != nil {
		t.Fatalf("details decode: %v", err)
	}
	if details["url"] != srv.URL {
		t.Fatalf("details.url = %v", details["url"])
	}
}

func TestHTTPDriver_NetworkError(t *testing.T) {
	d := NewHTTPDriver()
	// Port 1 is reserved and typically closed — ECONNREFUSED on loopback.
	env, err := d.Execute(context.Background(), httpTool("http://127.0.0.1:1", "GET", "", nil), nil, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want ok=false, got %+v", env)
	}
	if !strings.HasPrefix(env.Error, "http error:") {
		t.Fatalf("error = %q", env.Error)
	}
}

func TestHTTPDriver_Oversized(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), maxHTTPResponseBytes+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(payload)
	}))
	defer srv.Close()

	d := NewHTTPDriver()
	env, err := d.Execute(context.Background(), httpTool(srv.URL, "GET", "", nil), nil, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want ok=false, got %+v", env)
	}
	if env.Error != "http response oversized" {
		t.Fatalf("error = %q", env.Error)
	}
}

func TestHTTPDriver_URLPlaceholder(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	tool := crew.Tool{
		Name:     "ping",
		Protocol: crew.ToolHTTP,
		Method:   "GET",
		URL:      "{{input.base}}/ping",
	}
	d := NewHTTPDriver()
	env, err := d.Execute(context.Background(), tool, map[string]any{"base": srv.URL}, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !env.Ok {
		t.Fatalf("want ok=true, got %+v", env)
	}
	if gotPath != "/ping" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestHTTPDriver_HeaderPlaceholder(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	tool := crew.Tool{
		Name:     "ping",
		Protocol: crew.ToolHTTP,
		Method:   "GET",
		URL:      srv.URL,
		Headers:  map[string]string{"Authorization": "Bearer {{env.MYTOKEN}}"},
	}
	d := NewHTTPDriver()
	_, err := d.Execute(context.Background(), tool, nil, DriverContext{
		Env: map[string]string{"MYTOKEN": "abc"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if gotAuth != "Bearer abc" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestHTTPDriver_BodyPlaceholder(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	tool := crew.Tool{
		Name:     "ping",
		Protocol: crew.ToolHTTP,
		Method:   "POST",
		URL:      srv.URL,
		Body:     `{"x":"{{input.y}}"}`,
	}
	d := NewHTTPDriver()
	_, err := d.Execute(context.Background(), tool, map[string]any{"y": "z"}, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if gotBody != `{"x":"z"}` {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestHTTPDriver_DefaultMethodIsGET(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	tool := crew.Tool{
		Name:     "ping",
		Protocol: crew.ToolHTTP,
		URL:      srv.URL,
	}
	d := NewHTTPDriver()
	_, err := d.Execute(context.Background(), tool, nil, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if gotMethod != "GET" {
		t.Fatalf("method = %q", gotMethod)
	}
}

func TestHTTPDriver_POST(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	tool := crew.Tool{
		Name:     "ping",
		Protocol: crew.ToolHTTP,
		Method:   "POST",
		URL:      srv.URL,
		Body:     `hello`,
	}
	d := NewHTTPDriver()
	_, err := d.Execute(context.Background(), tool, nil, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if gotMethod != "POST" {
		t.Fatalf("method = %q", gotMethod)
	}
}

func TestHTTPDriver_RenderError(t *testing.T) {
	tool := crew.Tool{
		Name:     "ping",
		Protocol: crew.ToolHTTP,
		Method:   "GET",
		URL:      "http://example.invalid/{{input.missing}}",
	}
	d := NewHTTPDriver()
	env, err := d.Execute(context.Background(), tool, map[string]any{}, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want ok=false, got %+v", env)
	}
	if !strings.HasPrefix(env.Error, "http: render url failed") {
		t.Fatalf("error = %q", env.Error)
	}
}

// Sanity probe: document the agreed contract that successful 2xx with no
// body is a parse failure (envelope requires the "ok" field).
func TestHTTPDriver_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewHTTPDriver()
	env, err := d.Execute(context.Background(), httpTool(srv.URL, "GET", "", nil), nil, DriverContext{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if env.Ok {
		t.Fatalf("want ok=false, got %+v", env)
	}
	if env.Error != "invalid tool output" {
		t.Fatalf("error = %q", env.Error)
	}
}
