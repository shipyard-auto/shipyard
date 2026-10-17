package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
)

type fakeSubAPI struct {
	subs    []fairwayctl.Subscription
	deleted []string
	tested  []string
	allow   bool
}

func (f *fakeSubAPI) Close() error { return nil }
func (f *fakeSubAPI) SubscriptionList(_ context.Context, client string) ([]fairwayctl.Subscription, error) {
	var out []fairwayctl.Subscription
	for _, s := range f.subs {
		if client == "" || s.ClientID == client {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeSubAPI) SubscriptionAdd(_ context.Context, client, topic, url string, allowHTTP bool) (fairwayctl.Subscription, error) {
	f.allow = allowHTTP
	s := fairwayctl.Subscription{ID: "sub_1", ClientID: client, Topic: topic, URL: url}
	f.subs = append(f.subs, s)
	return s, nil
}
func (f *fakeSubAPI) SubscriptionDelete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *fakeSubAPI) SubscriptionTest(_ context.Context, id string) (string, error) {
	f.tested = append(f.tested, id)
	return "evt_1", nil
}

func runSubCmd(t *testing.T, api *fakeSubAPI, args ...string) (string, error) {
	t.Helper()
	cmd := newFairwaySubscriptionCmdWith(subscriptionDeps{
		route: routeDeps{socketPath: "/nonexistent", version: "test"},
		dial:  func(context.Context, fairwayctl.Opts) (subscriptionAPI, error) { return api, nil },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), err
}

func TestSubscriptionCommands(t *testing.T) {
	api := &fakeSubAPI{}
	out, err := runSubCmd(t, api, "add", "promo", "--topic", "whatsapp.message", "--url", "http://127.0.0.1:9100/h")
	if err != nil || !strings.Contains(out, "sub_1") || !strings.Contains(out, "subscription test sub_1") || api.allow {
		t.Fatalf("add: %q %v", out, err)
	}
	if _, err := runSubCmd(t, api, "add", "promo", "--topic", "x"); err == nil {
		t.Fatal("add without --url accepted")
	}
	if _, err := runSubCmd(t, api, "add", "promo", "--topic", "x", "--url", "http://10.0.0.1/h", "--allow-http"); err != nil || !api.allow {
		t.Fatal("--allow-http not forwarded")
	}
	api.subs[0].Stats.Delivered, api.subs[0].Stats.Failed = 3, 1
	api.subs[0].Stats.LastStatus, api.subs[0].Stats.LastAttemptAt = 200, time.Now()
	out, _ = runSubCmd(t, api, "list", "--client", "promo")
	if !strings.Contains(out, "whatsapp.message") || !strings.Contains(out, "200") || !strings.Contains(out, "3") {
		t.Fatalf("list:\n%s", out)
	}
	if out, _ = runSubCmd(t, api, "test", "sub_1"); !strings.Contains(out, "evt_1") || len(api.tested) != 1 {
		t.Fatalf("test: %q", out)
	}
	if _, err := runSubCmd(t, api, "rm", "sub_1"); err != nil || api.deleted[0] != "sub_1" {
		t.Fatal("rm")
	}
}
