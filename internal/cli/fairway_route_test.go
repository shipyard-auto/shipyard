package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
)

type fakeRouteClient struct {
	routes       []fairwayctl.Route
	listErr      error
	addErr       error
	deleteErr    error
	testErr      error
	testResult   fairwayctl.TestResult
	addCalls     int
	deleteCalls  int
	testCalls    int
	lastAdded    fairwayctl.Route
	lastDeleted  string
	lastTestPath string
	lastMethod   string
	lastBody     []byte
	lastHeaders  map[string]string
}

func (f *fakeRouteClient) Close() error { return nil }
func (f *fakeRouteClient) RouteList(context.Context) ([]fairwayctl.Route, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.routes, nil
}
func (f *fakeRouteClient) RouteAdd(_ context.Context, route fairwayctl.Route) error {
	f.addCalls++
	f.lastAdded = route
	return f.addErr
}
func (f *fakeRouteClient) RouteDelete(_ context.Context, path string) error {
	f.deleteCalls++
	f.lastDeleted = path
	return f.deleteErr
}
func (f *fakeRouteClient) RouteTest(_ context.Context, path, method string, body []byte, headers map[string]string) (fairwayctl.TestResult, error) {
	f.testCalls++
	f.lastTestPath = path
	f.lastMethod = method
	f.lastBody = append([]byte(nil), body...)
	f.lastHeaders = headers
	if f.testErr != nil {
		return fairwayctl.TestResult{}, f.testErr
	}
	return f.testResult, nil
}

func baseRouteDeps(client routeClient) routeDeps {
	return routeDeps{
		version:    "0.21",
		socketPath: "/tmp/fairway.sock",
		dial: func(context.Context, fairwayctl.Opts) (routeClient, error) {
			return client, nil
		},
		readFile:      os.ReadFile,
		stdin:         strings.NewReader(""),
		stdinFD:       func() uintptr { return 0 },
		isInteractive: func(uintptr) bool { return false },
	}
}

func TestParseAdd_bearer_tokenFlag(t *testing.T) {
	route, err := buildRouteFromAddInput(addRouteInput{
		path:       "/hooks/github",
		authType:   "bearer",
		authToken:  "secret",
		actionType: "cron.run",
		target:     "ABC123",
	}, routeDeps{})
	if err != nil {
		t.Fatalf("buildRouteFromAddInput() error = %v", err)
	}
	if route.Auth.Type != fairwayctl.AuthBearer || route.Auth.Token != "secret" {
		t.Fatalf("route.Auth = %+v", route.Auth)
	}
}

func TestParseAdd_token_headerFlag(t *testing.T) {
	route, err := buildRouteFromAddInput(addRouteInput{
		path:       "/hooks/telegram",
		authType:   "token",
		authValue:  "secret",
		authHeader: "X-Token",
		actionType: "telegram.handle",
		target:     "handle",
	}, routeDeps{})
	if err != nil {
		t.Fatalf("buildRouteFromAddInput() error = %v", err)
	}
	if route.Auth.Type != fairwayctl.AuthToken || route.Auth.Header != "X-Token" || route.Auth.Value != "secret" {
		t.Fatalf("route.Auth = %+v", route.Auth)
	}
}

func TestParseAdd_localOnly_disallowsToken(t *testing.T) {
	route, err := buildRouteFromAddInput(addRouteInput{
		path:       "/internal/events",
		authType:   "local-only",
		authToken:  "forbidden",
		actionType: "message.send",
		target:     "event.dispatch",
	}, routeDeps{})
	if err != nil {
		t.Fatalf("buildRouteFromAddInput() error = %v", err)
	}
	if err := route.Validate(); !errors.Is(err, fairwayctl.ErrLocalOnlyExtraField) {
		t.Fatalf("Validate() error = %v; want ErrLocalOnlyExtraField", err)
	}
}

func TestParseAdd_asyncFlag_setsAsyncTrue(t *testing.T) {
	route, err := buildRouteFromAddInput(addRouteInput{
		path:       "/hooks/agent",
		authType:   "local-only",
		actionType: "crew.run",
		target:     "agent-x",
		async:      true,
	}, routeDeps{})
	if err != nil {
		t.Fatalf("buildRouteFromAddInput() error = %v", err)
	}
	if !route.Async {
		t.Fatalf("route.Async = false; want true when --async is set")
	}
}

func TestParseAdd_asyncDefault_false(t *testing.T) {
	route, err := buildRouteFromAddInput(addRouteInput{
		path:       "/hooks/agent",
		authType:   "local-only",
		actionType: "crew.run",
		target:     "agent-x",
	}, routeDeps{})
	if err != nil {
		t.Fatalf("buildRouteFromAddInput() error = %v", err)
	}
	if route.Async {
		t.Fatalf("route.Async = true; want false by default")
	}
}

func TestParseAdd_invalidTimeout_error(t *testing.T) {
	_, err := buildRouteFromAddInput(addRouteInput{
		path:       "/hooks/github",
		authType:   "bearer",
		authToken:  "secret",
		actionType: "cron.run",
		target:     "ABC123",
		timeout:    "not-a-duration",
	}, routeDeps{})
	if err == nil || !strings.Contains(err.Error(), "invalid --timeout") {
		t.Fatalf("error = %v; want invalid --timeout", err)
	}
}

func TestParseAdd_missingPath_error(t *testing.T) {
	_, err := buildRouteFromAddInput(addRouteInput{
		authType:   "bearer",
		authToken:  "secret",
		actionType: "cron.run",
		target:     "ABC123",
	}, routeDeps{})
	if err == nil || !strings.Contains(err.Error(), "--path") {
		t.Fatalf("error = %v; want missing --path", err)
	}
}

func TestParseAdd_fromFile_readsJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "route.json")
	if err := os.WriteFile(path, []byte(`{"path":"/hooks/github","auth":{"type":"bearer","token":"secret"},"action":{"type":"cron.run","target":"ABC123"},"timeout":30000000000}`), 0600); err != nil {
		t.Fatal(err)
	}
	route, err := buildRouteFromAddInput(addRouteInput{fromFile: path}, routeDeps{readFile: os.ReadFile})
	if err != nil {
		t.Fatalf("buildRouteFromAddInput() error = %v", err)
	}
	if route.Path != "/hooks/github" || route.Timeout != 30*time.Second {
		t.Fatalf("route = %+v", route)
	}
}

func TestParseAdd_fromFile_invalidJSON_error(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "route.json")
	if err := os.WriteFile(path, []byte(`{invalid`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := buildRouteFromAddInput(addRouteInput{fromFile: path}, routeDeps{readFile: os.ReadFile})
	if err == nil || !strings.Contains(err.Error(), "parse route file") {
		t.Fatalf("error = %v; want parse route file", err)
	}
}

func TestList_rendersTable(t *testing.T) {
	client := &fakeRouteClient{
		routes: []fairwayctl.Route{
			{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "ABC123"}, Timeout: 30 * time.Second},
			{Path: "/hooks/telegram", Auth: fairwayctl.Auth{Type: fairwayctl.AuthToken}, Action: fairwayctl.Action{Type: fairwayctl.ActionTelegramHandle, Target: "telegram.handle"}},
		},
	}
	cmd := newFairwayRouteListCmdWith(baseRouteDeps(client))
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "PATH") || !strings.Contains(out, "/hooks/github") || !strings.Contains(out, "30s") {
		t.Fatalf("output = %q", out)
	}
}

func TestList_json_rendersJSON(t *testing.T) {
	client := &fakeRouteClient{
		routes: []fairwayctl.Route{
			{Path: "/hooks/github", Auth: fairwayctl.Auth{Type: fairwayctl.AuthBearer}, Action: fairwayctl.Action{Type: fairwayctl.ActionCronRun, Target: "ABC123"}},
		},
	}
	cmd := newFairwayRouteListCmdWith(baseRouteDeps(client))
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var routes []fairwayctl.Route
	if err := json.Unmarshal(buf.Bytes(), &routes); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(routes) != 1 || routes[0].Path != "/hooks/github" {
		t.Fatalf("routes = %+v", routes)
	}
}

func TestList_empty_rendersEmptyMessage(t *testing.T) {
	client := &fakeRouteClient{}
	cmd := newFairwayRouteListCmdWith(baseRouteDeps(client))
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(buf.String(), "No fairway routes configured.") {
		t.Fatalf("output = %q", buf.String())
	}
}

func TestAdd_success_printsConfirmation(t *testing.T) {
	client := &fakeRouteClient{}
	cmd := newFairwayRouteAddCmdWith(baseRouteDeps(client))
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	mustSet(t, cmd, "path", "/hooks/github")
	mustSet(t, cmd, "auth", "bearer")
	mustSet(t, cmd, "auth-token", "secret")
	mustSet(t, cmd, "action", "cron.run")
	mustSet(t, cmd, "target", "ABC123")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if client.addCalls != 1 || client.lastAdded.Path != "/hooks/github" {
		t.Fatalf("client.addCalls = %d route = %+v", client.addCalls, client.lastAdded)
	}
	if !strings.Contains(buf.String(), "Added fairway route") {
		t.Fatalf("output = %q", buf.String())
	}
}

func TestAdd_duplicate_printsError_exit1(t *testing.T) {
	client := &fakeRouteClient{addErr: fairwayctl.ErrDuplicatePath}
	cmd := newFairwayRouteAddCmdWith(baseRouteDeps(client))
	mustSet(t, cmd, "path", "/hooks/github")
	mustSet(t, cmd, "auth", "bearer")
	mustSet(t, cmd, "auth-token", "secret")
	mustSet(t, cmd, "action", "cron.run")
	mustSet(t, cmd, "target", "ABC123")
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "route list") {
		t.Fatalf("error = %v; want actionable duplicate message", err)
	}
}

func TestAdd_invalidLocally_rejectedWithoutSocketCall(t *testing.T) {
	dialCalls := 0
	deps := routeDeps{
		dial: func(context.Context, fairwayctl.Opts) (routeClient, error) {
			dialCalls++
			return &fakeRouteClient{}, nil
		},
	}
	cmd := newFairwayRouteAddCmdWith(deps)
	mustSet(t, cmd, "path", "/internal/events")
	mustSet(t, cmd, "auth", "local-only")
	mustSet(t, cmd, "auth-token", "forbidden")
	mustSet(t, cmd, "action", "message.send")
	mustSet(t, cmd, "target", "event.dispatch")
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected local validation error, got nil")
	}
	if dialCalls != 0 {
		t.Fatalf("dialCalls = %d; want 0", dialCalls)
	}
}

func TestDelete_confirmationInteractive(t *testing.T) {
	client := &fakeRouteClient{}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	go func() {
		_, _ = writer.Write([]byte("y\n"))
		_ = writer.Close()
	}()
	deps := baseRouteDeps(client)
	deps.stdin = reader
	deps.stdinFD = func() uintptr { return 123 }
	deps.isInteractive = func(uintptr) bool { return true }

	cmd := newFairwayRouteDeleteCmdWith(deps)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"/hooks/github"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if client.deleteCalls != 1 {
		t.Fatalf("deleteCalls = %d; want 1", client.deleteCalls)
	}
	if !strings.Contains(buf.String(), "Confirma remover /hooks/github?") {
		t.Fatalf("output = %q", buf.String())
	}
}

func TestDelete_yesFlag_bypassesPrompt(t *testing.T) {
	client := &fakeRouteClient{}
	deps := baseRouteDeps(client)
	deps.isInteractive = func(uintptr) bool { return true }
	cmd := newFairwayRouteDeleteCmdWith(deps)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"/hooks/github"})
	mustSet(t, cmd, "yes", "true")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(buf.String(), "Confirma remover") {
		t.Fatalf("output = %q; prompt should be bypassed", buf.String())
	}
	if client.deleteCalls != 1 {
		t.Fatalf("deleteCalls = %d; want 1", client.deleteCalls)
	}
}

func TestDelete_notFound_printsError_exit1(t *testing.T) {
	client := &fakeRouteClient{deleteErr: fairwayctl.ErrRouteNotFound}
	cmd := newFairwayRouteDeleteCmdWith(baseRouteDeps(client))
	cmd.SetArgs([]string{"/hooks/missing"})
	mustSet(t, cmd, "yes", "true")
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "route list") {
		t.Fatalf("error = %v; want actionable not found message", err)
	}
}

func TestTest_outputsStatusAndBody(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	client := &fakeRouteClient{testResult: fairwayctl.TestResult{Status: 200, Body: "OK"}}
	cmd := newFairwayRouteTestCmdWith(baseRouteDeps(client))
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"/hooks/github"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(buf.String(), "Status: 200") || !strings.Contains(buf.String(), "OK") {
		t.Fatalf("output = %q", buf.String())
	}
}

func TestTest_bodyFile_readsFromDisk(t *testing.T) {
	client := &fakeRouteClient{testResult: fairwayctl.TestResult{Status: 202, Body: "accepted"}}
	dir := t.TempDir()
	bodyPath := filepath.Join(dir, "body.json")
	if err := os.WriteFile(bodyPath, []byte(`{"ok":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newFairwayRouteTestCmdWith(baseRouteDeps(client))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"/hooks/github"})
	mustSet(t, cmd, "body-file", bodyPath)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(client.lastBody) != `{"ok":true}` {
		t.Fatalf("lastBody = %q", string(client.lastBody))
	}
}

func TestTest_headers_parsedCorrectly(t *testing.T) {
	client := &fakeRouteClient{testResult: fairwayctl.TestResult{Status: 200, Body: "OK"}}
	cmd := newFairwayRouteTestCmdWith(baseRouteDeps(client))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"/hooks/github"})
	if err := cmd.Flags().Set("header", "X-One=1"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("header", "X-Two=2"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if client.lastHeaders["X-One"] != "1" || client.lastHeaders["X-Two"] != "2" {
		t.Fatalf("headers = %+v", client.lastHeaders)
	}
}

func TestTest_invalidHeader_error(t *testing.T) {
	cmd := newFairwayRouteTestCmdWith(baseRouteDeps(&fakeRouteClient{}))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"/hooks/github"})
	mustSet(t, cmd, "header", "sem-equal")
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "expected KEY=VALUE") {
		t.Fatalf("error = %v", err)
	}
}

func TestRouteCommands_integrationFakeDaemonCycle(t *testing.T) {
	dir, err := os.MkdirTemp("", "fwroute")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck
	sockPath := filepath.Join(dir, "fairway.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var mu sync.Mutex
	routes := []fairwayctl.Route{}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveRouteCycleConn(conn, &mu, &routes)
		}
	}()

	deps := routeDeps{
		version:    "0.21",
		socketPath: sockPath,
		dial: func(ctx context.Context, opts fairwayctl.Opts) (routeClient, error) {
			return fairwayctl.Dial(ctx, opts)
		},
	}

	addCmd := newFairwayRouteAddCmdWith(deps)
	addOut := &bytes.Buffer{}
	addCmd.SetOut(addOut)
	mustSet(t, addCmd, "path", "/hooks/github")
	mustSet(t, addCmd, "auth", "bearer")
	mustSet(t, addCmd, "auth-token", "secret")
	mustSet(t, addCmd, "action", "cron.run")
	mustSet(t, addCmd, "target", "ABC123")
	if err := addCmd.Execute(); err != nil {
		t.Fatalf("add Execute() error = %v", err)
	}

	listCmd := newFairwayRouteListCmdWith(deps)
	listOut := &bytes.Buffer{}
	listCmd.SetOut(listOut)
	if err := listCmd.Execute(); err != nil {
		t.Fatalf("list Execute() error = %v", err)
	}
	if !strings.Contains(listOut.String(), "/hooks/github") {
		t.Fatalf("list output = %q", listOut.String())
	}

	deleteCmd := newFairwayRouteDeleteCmdWith(deps)
	deleteCmd.SetOut(&bytes.Buffer{})
	deleteCmd.SetArgs([]string{"/hooks/github"})
	mustSet(t, deleteCmd, "yes", "true")
	if err := deleteCmd.Execute(); err != nil {
		t.Fatalf("delete Execute() error = %v", err)
	}

	listOut.Reset()
	if err := listCmd.Execute(); err != nil {
		t.Fatalf("second list Execute() error = %v", err)
	}
	if !strings.Contains(listOut.String(), "No fairway routes configured.") {
		t.Fatalf("second list output = %q", listOut.String())
	}
}

func serveRouteCycleConn(conn net.Conn, mu *sync.Mutex, routes *[]fairwayctl.Route) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if first {
			first = false
			var req struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(line, &req)
			resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]string{"daemonVersion": "0.21"}})
			_, _ = conn.Write(append(resp, '\n'))
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(line, &req)
		var resp []byte
		switch req.Method {
		case "route.add":
			var params struct {
				Route fairwayctl.Route `json:"route"`
			}
			_ = json.Unmarshal(req.Params, &params)
			mu.Lock()
			*routes = append(*routes, params.Route)
			mu.Unlock()
			resp, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]bool{"ok": true}})
		case "route.list":
			mu.Lock()
			current := append([]fairwayctl.Route(nil), (*routes)...)
			mu.Unlock()
			resp, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": current})
		case "route.delete":
			var params struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(req.Params, &params)
			mu.Lock()
			next := (*routes)[:0]
			for _, route := range *routes {
				if route.Path != params.Path {
					next = append(next, route)
				}
			}
			*routes = append([]fairwayctl.Route(nil), next...)
			mu.Unlock()
			resp, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]bool{"ok": true}})
		default:
			resp, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
		_, _ = conn.Write(append(resp, '\n'))
	}
}

func mustSet(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("Flags().Set(%s): %v", name, err)
	}
}
