package fairway

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultSocketPath(t *testing.T) {
	t.Run("respectsShipyardHome", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("SHIPYARD_HOME", root)
		path, err := DefaultSocketPath()
		if err != nil {
			t.Fatalf("DefaultSocketPath() error = %v", err)
		}
		want := filepath.Join(root, "run", "fairway.sock")
		if path != want {
			t.Fatalf("path = %q, want %q", path, want)
		}
	})

	t.Run("fallsBackToHome", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("SHIPYARD_HOME", "")
		t.Setenv("HOME", root)
		path, err := DefaultSocketPath()
		if err != nil {
			t.Fatalf("DefaultSocketPath() error = %v", err)
		}
		want := filepath.Join(root, ".shipyard", "run", "fairway.sock")
		if path != want {
			t.Fatalf("path = %q, want %q", path, want)
		}
	})
}

func TestNewSocketServer(t *testing.T) {
	t.Run("rejectsNilRouter", func(t *testing.T) {
		_, err := NewSocketServer(SocketServerConfig{})
		if err == nil {
			t.Fatal("NewSocketServer() error = nil, want error")
		}
	})

	t.Run("fillsDefaults", func(t *testing.T) {
		server := mustNewSocketServer(t, baseSocketRouterConfig(), "")
		if server.version != "dev" {
			t.Fatalf("version = %q, want dev", server.version)
		}
		if server.handshakeTimeout != defaultHandshakeTimeout {
			t.Fatalf("handshakeTimeout = %s, want %s", server.handshakeTimeout, defaultHandshakeTimeout)
		}
		if server.Errors() == nil {
			t.Fatal("Errors() = nil, want channel")
		}
	})
}

func TestSocketServerHandshake(t *testing.T) {
	server := mustNewSocketServer(t, baseSocketRouterConfig(), "0.22")

	t.Run("requiresHandshakeFirst", func(t *testing.T) {
		client, conn := net.Pipe()
		defer client.Close()
		go server.serveConn(conn)

		writeLine(t, client, `{"jsonrpc":"2.0","id":"1","method":"route.list","params":{}}`)
		response := readResponse(t, client)
		if response.Error == nil || response.Error.Code != errCodeHandshakeRequired {
			t.Fatalf("response = %#v, want handshake required error", response)
		}
	})

	t.Run("invalidHandshakeParams", func(t *testing.T) {
		client, conn := net.Pipe()
		defer client.Close()
		go server.serveConn(conn)

		writeLine(t, client, `{"jsonrpc":"2.0","id":"0","method":"handshake","params":"bad"}`)
		response := readResponse(t, client)
		if response.Error == nil || response.Error.Code != errCodeInvalidParams {
			t.Fatalf("response = %#v, want invalid params", response)
		}
	})

	t.Run("versionMismatchClosesConnection", func(t *testing.T) {
		client, conn := net.Pipe()
		defer client.Close()
		go server.serveConn(conn)

		writeLine(t, client, `{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.21"}}`)
		response := readResponse(t, client)
		if response.Error == nil || response.Error.Code != errCodeVersionMismatch {
			t.Fatalf("response = %#v, want version mismatch", response)
		}
	})

	t.Run("successfulHandshakeAllowsSubsequentCalls", func(t *testing.T) {
		client, conn := net.Pipe()
		defer client.Close()
		go server.serveConn(conn)

		writeLine(t, client, `{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}`)
		handshake := readResponse(t, client)
		if handshake.Error != nil {
			t.Fatalf("handshake = %#v, want success", handshake)
		}

		writeLine(t, client, `{"jsonrpc":"2.0","id":"1","method":"route.list","params":{}}`)
		response := readResponse(t, client)
		if response.Error != nil {
			t.Fatalf("response = %#v, want success", response)
		}
	})

	t.Run("handshakeTimeoutClosesConnection", func(t *testing.T) {
		timeoutServer := mustNewSocketServer(t, baseSocketRouterConfig(), "0.22")
		timeoutServer.handshakeTimeout = 50 * time.Millisecond

		client, conn := net.Pipe()
		defer client.Close()
		go timeoutServer.serveConn(conn)

		time.Sleep(100 * time.Millisecond)
		if _, err := client.Write([]byte(`{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}` + "\n")); err == nil {
			t.Fatal("write succeeded after handshake timeout, want closed connection")
		}
	})
}

func TestSocketServerMethods(t *testing.T) {
	server := mustNewSocketServer(t, baseSocketRouterConfig(), "0.22")

	t.Run("routeList", func(t *testing.T) {
		response := rpcRoundTrip(t, server,
			`{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}`,
			`{"jsonrpc":"2.0","id":"1","method":"route.list","params":{}}`,
		)
		if response.Error != nil {
			t.Fatalf("response = %#v, want success", response)
		}
		raw, _ := json.Marshal(response.Result)
		if !strings.Contains(string(raw), `/hooks/github`) {
			t.Fatalf("result = %s, want /hooks/github", string(raw))
		}
	})

	t.Run("routeAddDeleteReplaceAndStatus", func(t *testing.T) {
		client, conn := net.Pipe()
		defer client.Close()
		go server.serveConn(conn)

		writeLine(t, client, `{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}`)
		_ = readResponse(t, client)

		writeLine(t, client, `{"jsonrpc":"2.0","id":"1","method":"route.add","params":{"path":"/hooks/new","auth":{"type":"bearer","token":"secret"},"action":{"type":"cron.run","target":"job-2"}}}`)
		addResp := readResponse(t, client)
		if addResp.Error != nil {
			t.Fatalf("addResp = %#v, want success", addResp)
		}

		writeLine(t, client, `{"jsonrpc":"2.0","id":"2","method":"status","params":{}}`)
		statusResp := readResponse(t, client)
		if statusResp.Error != nil {
			t.Fatalf("statusResp = %#v, want success", statusResp)
		}
		statusRaw, _ := json.Marshal(statusResp.Result)
		if !strings.Contains(string(statusRaw), `"routeCount":2`) {
			t.Fatalf("status = %s, want routeCount 2", string(statusRaw))
		}
		if !strings.Contains(string(statusRaw), `"pid":`) {
			t.Fatalf("status = %s, want pid field", string(statusRaw))
		}

		writeLine(t, client, `{"jsonrpc":"2.0","id":"3","method":"route.replace","params":{"route":{"path":"/hooks/new","auth":{"type":"bearer","token":"secret"},"action":{"type":"cron.run","target":"job-3"}}}}`)
		replaceResp := readResponse(t, client)
		if replaceResp.Error != nil {
			t.Fatalf("replaceResp = %#v, want success", replaceResp)
		}

		writeLine(t, client, `{"jsonrpc":"2.0","id":"4","method":"route.delete","params":{"path":"/hooks/new"}}`)
		deleteResp := readResponse(t, client)
		if deleteResp.Error != nil {
			t.Fatalf("deleteResp = %#v, want success", deleteResp)
		}
	})

	t.Run("methodNotFound", func(t *testing.T) {
		response := rpcRoundTrip(t, server,
			`{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}`,
			`{"jsonrpc":"2.0","id":"1","method":"unknown.method","params":{}}`,
		)
		if response.Error == nil || response.Error.Code != errCodeMethodNotFound {
			t.Fatalf("response = %#v, want method not found", response)
		}
	})

	t.Run("invalidJSONRPCVersion", func(t *testing.T) {
		response := rpcRoundTrip(t, server,
			`{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}`,
			`{"jsonrpc":"1.0","id":"1","method":"route.list","params":{}}`,
		)
		if response.Error == nil || response.Error.Code != errCodeInvalidRequest {
			t.Fatalf("response = %#v, want invalid request", response)
		}
	})

	t.Run("invalidRouteParams", func(t *testing.T) {
		response := rpcRoundTrip(t, server,
			`{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}`,
			`{"jsonrpc":"2.0","id":"1","method":"route.add","params":"bad"}`,
		)
		if response.Error == nil || response.Error.Code != errCodeInvalidParams {
			t.Fatalf("response = %#v, want invalid params", response)
		}
	})

	t.Run("invalidDeleteParams", func(t *testing.T) {
		response := rpcRoundTrip(t, server,
			`{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}`,
			`{"jsonrpc":"2.0","id":"1","method":"route.delete","params":{}}`,
		)
		if response.Error == nil || response.Error.Code != errCodeInvalidParams {
			t.Fatalf("response = %#v, want invalid params", response)
		}
	})

	t.Run("invalidReplaceParams", func(t *testing.T) {
		response := rpcRoundTrip(t, server,
			`{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}`,
			`{"jsonrpc":"2.0","id":"1","method":"route.replace","params":"bad"}`,
		)
		if response.Error == nil || response.Error.Code != errCodeInvalidParams {
			t.Fatalf("response = %#v, want invalid params", response)
		}
	})

	t.Run("statusUsesInjectedProvider", func(t *testing.T) {
		server, err := NewSocketServer(SocketServerConfig{
			Router:  NewRouterWithConfig(&fakeRepository{}, baseSocketRouterConfig()),
			Version: "0.22",
			Status: staticStatusProvider{
				snapshot: StatusSnapshot{
					Version:     "9.9.9",
					Bind:        "127.0.0.2",
					Port:        4242,
					RouteCount:  7,
					PID:         12345,
					ConfigPath:  "/tmp/routes.json",
					SocketPath:  "/tmp/fairway.sock",
					PIDFilePath: "/tmp/fairway.pid",
					StartedAt:   time.Date(2026, 4, 17, 1, 40, 0, 0, time.UTC),
				},
			},
		})
		if err != nil {
			t.Fatalf("NewSocketServer() error = %v", err)
		}

		response := rpcRoundTrip(t, server,
			`{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}`,
			`{"jsonrpc":"2.0","id":"1","method":"status","params":{}}`,
		)
		if response.Error != nil {
			t.Fatalf("response = %#v, want success", response)
		}

		raw, _ := json.Marshal(response.Result)
		text := string(raw)
		for _, needle := range []string{
			`"version":"9.9.9"`,
			`"bind":"127.0.0.2"`,
			`"port":4242`,
			`"routeCount":7`,
			`"pid":12345`,
			`"configPath":"/tmp/routes.json"`,
			`"socketPath":"/tmp/fairway.sock"`,
			`"pidFilePath":"/tmp/fairway.pid"`,
		} {
			if !strings.Contains(text, needle) {
				t.Fatalf("status = %s, want %s", text, needle)
			}
		}
	})
}

func TestSocketServerStartAndShutdown(t *testing.T) {
	server := mustNewSocketServer(t, baseSocketRouterConfig(), "0.22")
	dir, err := os.MkdirTemp("/tmp", "fairway-sock-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "fairway.sock")

	if err := server.Start(socketPath); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer server.Shutdown()

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 600", got)
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	writeLine(t, conn, `{"jsonrpc":"2.0","id":"0","method":"handshake","params":{"version":"0.22"}}`)
	response := readResponse(t, conn)
	if response.Error != nil {
		t.Fatalf("response = %#v, want success", response)
	}

	t.Run("shutdownWithoutListener", func(t *testing.T) {
		server := mustNewSocketServer(t, baseSocketRouterConfig(), "0.22")
		if err := server.Shutdown(); err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	})
}

func TestSocketHelpers(t *testing.T) {
	t.Run("decodeRouteParamsSupportsWrappedRoute", func(t *testing.T) {
		var route Route
		err := decodeRouteParams(json.RawMessage(`{"route":{"path":"/hooks/new","auth":{"type":"bearer","token":"secret"},"action":{"type":"cron.run","target":"job-1"}}}`), &route)
		if err != nil {
			t.Fatalf("decodeRouteParams() error = %v", err)
		}
		if route.Path != "/hooks/new" {
			t.Fatalf("route.Path = %q, want /hooks/new", route.Path)
		}
	})

	t.Run("decodeRouteParamsMissingParams", func(t *testing.T) {
		var route Route
		if err := decodeRouteParams(nil, &route); err == nil {
			t.Fatal("decodeRouteParams() error = nil, want error")
		}
	})

	t.Run("decodeIDFallback", func(t *testing.T) {
		got := decodeID(json.RawMessage(`{`))
		if got != "{" {
			t.Fatalf("decodeID() = %#v, want %q", got, "{")
		}
	})

	t.Run("writeRPCResponseMarshalError", func(t *testing.T) {
		var builder strings.Builder
		writer := bufio.NewWriter(&builder)
		err := writeRPCResponse(writer, rpcResponse{
			JSONRPC: jsonRPCVersion,
			Result:  make(chan int),
		})
		if err == nil {
			t.Fatal("writeRPCResponse() error = nil, want error")
		}
	})
}

func mustNewSocketServer(t *testing.T, cfg Config, version string) *SocketServer {
	t.Helper()
	router := NewRouterWithConfig(&fakeRepository{}, cfg)
	server, err := NewSocketServer(SocketServerConfig{
		Router:  router,
		Version: version,
	})
	if err != nil {
		t.Fatalf("NewSocketServer() error = %v", err)
	}
	return server
}

type staticStatusProvider struct {
	snapshot StatusSnapshot
}

func (s staticStatusProvider) Status() StatusSnapshot {
	return s.snapshot
}

func baseSocketRouterConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Port:          DefaultPort,
		Bind:          DefaultBind,
		Routes: []Route{
			{
				Path:   "/hooks/github",
				Auth:   Auth{Type: AuthBearer, Token: "secret"},
				Action: Action{Type: ActionCronRun, Target: "job-1"},
			},
		},
	}
}

func rpcRoundTrip(t *testing.T, server *SocketServer, lines ...string) rpcResponse {
	t.Helper()
	client, conn := net.Pipe()
	defer client.Close()
	go server.serveConn(conn)

	var response rpcResponse
	for i, line := range lines {
		writeLine(t, client, line)
		response = readResponse(t, client)
		if i == 0 && response.Error != nil {
			t.Fatalf("handshake response = %#v, want success", response)
		}
	}
	return response
}

func writeLine(t *testing.T, conn net.Conn, line string) {
	t.Helper()
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

func readResponse(t *testing.T, conn net.Conn) rpcResponse {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("ReadBytes() error = %v", err)
	}
	var response rpcResponse
	if err := json.Unmarshal(bytesTrimSpace(line), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return response
}
