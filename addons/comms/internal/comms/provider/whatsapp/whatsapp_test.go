package whatsapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
)

// fakeDaemon speaks the daemon's socket contract: it records each request
// and answers with reply(params).
func fakeDaemon(t *testing.T, reply func(params map[string]any) string) (string, <-chan map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wa-prov-") // short path: unix socket limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "wa.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	reqs := make(chan map[string]any, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(conn)
			if sc.Scan() {
				var req struct {
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				_ = json.Unmarshal(sc.Bytes(), &req)
				req.Params["_method"] = req.Method
				reqs <- req.Params
				_, _ = conn.Write([]byte(reply(req.Params) + "\n"))
			}
			_ = conn.Close()
		}
	}()
	return sock, reqs
}

func msg(to, body string, meta map[string]string) comms.Message {
	return comms.Message{SchemaVersion: 1, Channel: "c", Direction: comms.DirectionOutbound,
		From: comms.Address{Channel: "c", Raw: "c"}, To: []comms.Address{{Channel: "c", Raw: to}},
		Body: body, Metadata: meta, Timestamp: time.Now()}
}

func TestSendForwardsToDaemon(t *testing.T) {
	sock, reqs := fakeDaemon(t, func(map[string]any) string {
		return `{"jsonrpc":"2.0","id":1,"result":{"msg_id":"3EB0ABC","sent_at":"2026-10-17T17:14:15Z","chat":"x"}}`
	})
	p := &Provider{SocketPath: func() (string, error) { return sock, nil }}
	rc, err := p.Send(context.Background(), comms.Channel{}, nil,
		msg("+55 (12) 98201-6477", "Fala Vini!", map[string]string{MetadataIdempotencyKey: "k1"}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.ProviderMessageID != "3EB0ABC" || rc.SentAt.IsZero() {
		t.Fatalf("receipt=%+v", rc)
	}
	got := <-reqs
	if got["_method"] != "send" || got["chat"] != "5512982016477@s.whatsapp.net" || got["text"] != "Fala Vini!" || got["idempotency_key"] != "k1" {
		t.Fatalf("request=%v", got)
	}
}

func TestSendMapsRefusals(t *testing.T) {
	sock, _ := fakeDaemon(t, func(map[string]any) string {
		return `{"jsonrpc":"2.0","id":1,"error":{"code":-32012,"message":"no conversation with Vini yet","data":{"code":"no_history"}}}`
	})
	// Channel setting overrides the provider default.
	ch := comms.Channel{Settings: map[string]any{SettingSocket: sock}}
	_, err := New().Send(context.Background(), ch, nil, msg("Vini Eduqo", "oi", nil))
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Code != "no_history" {
		t.Fatalf("err=%v", err)
	}
}

func TestSendValidationAndMissingDaemon(t *testing.T) {
	p := &Provider{SocketPath: func() (string, error) { return filepath.Join(t.TempDir(), "absent.sock"), nil }}
	if _, err := p.Send(context.Background(), comms.Channel{}, nil, msg("x", "oi", nil)); !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("no daemon: %v", err)
	}
	m := msg("x", "oi", nil)
	m.To = append(m.To, comms.Address{Raw: "y"})
	if _, err := p.Send(context.Background(), comms.Channel{}, nil, m); err == nil {
		t.Fatal("two recipients accepted")
	}
	if _, err := p.Send(context.Background(), comms.Channel{}, nil, msg("x", "", nil)); err == nil {
		t.Fatal("empty body accepted")
	}
}

func TestNormalizeRecipient(t *testing.T) {
	cases := map[string]string{
		"+55 (12) 98201-6477":          "5512982016477@s.whatsapp.net",
		"5512982016477@s.whatsapp.net": "5512982016477@s.whatsapp.net",
		"120363@g.us":                  "120363@g.us",
		"Vini Eduqo":                   "Vini Eduqo",
		"012345678":                    "012345678",
		"1234":                         "1234",
	}
	for in, want := range cases {
		if got := NormalizeRecipient(in); got != want {
			t.Errorf("NormalizeRecipient(%q)=%q want %q", in, got, want)
		}
	}
}
