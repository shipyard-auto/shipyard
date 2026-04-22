package crew

import (
	"strings"
	"testing"
)

func validAgent() Agent {
	return Agent{
		Name:        "promo-hunter",
		Description: "desc",
		Backend: Backend{
			Type:    BackendCLI,
			Command: []string{"claude", "--print"},
		},
		Execution: Execution{
			Mode: ExecutionOnDemand,
			Pool: "cli",
		},
		Conversation: Conversation{
			Mode: ConversationStateless,
		},
		Triggers: []Trigger{
			{Type: TriggerCron, Schedule: "0 */3 * * *"},
		},
		Tools: []Tool{
			{
				Name:     "scraper",
				Protocol: ToolExec,
				Command:  []string{"/bin/true"},
			},
		},
	}
}

func TestAgentValidate(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(*Agent)
		wantErr string
	}{
		{"happy", func(a *Agent) {}, ""},
		{"empty name", func(a *Agent) { a.Name = "" }, "agent.name"},
		{"uppercase name", func(a *Agent) { a.Name = "Promo" }, "agent.name"},
		{"name too long", func(a *Agent) { a.Name = strings.Repeat("a", 64) }, "agent.name"},
		{"backend invalid", func(a *Agent) { a.Backend.Command = nil }, "agent.backend"},
		{"execution invalid", func(a *Agent) { a.Execution.Mode = "" }, "agent.execution"},
		{"conversation invalid", func(a *Agent) { a.Conversation.Mode = "" }, "agent.conversation"},
		{"trigger invalid", func(a *Agent) { a.Triggers[0].Schedule = "" }, "agent.triggers[0]"},
		{"tool invalid", func(a *Agent) { a.Tools[0].Command = nil }, "agent.tools[0]"},
		{"duplicate tool name", func(a *Agent) {
			a.Tools = append(a.Tools, Tool{Name: "scraper", Protocol: ToolExec, Command: []string{"/bin/true"}})
		}, "duplicate tool name"},
		{"duplicate trigger", func(a *Agent) {
			a.Triggers = append(a.Triggers, Trigger{Type: TriggerCron, Schedule: "0 */3 * * *"})
		}, "duplicate trigger"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := validAgent()
			tc.mut(&a)
			err := a.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestBackendValidate(t *testing.T) {
	tests := []struct {
		name    string
		b       Backend
		wantErr string
	}{
		{"cli ok", Backend{Type: BackendCLI, Command: []string{"x"}}, ""},
		{"cli no command", Backend{Type: BackendCLI}, "command"},
		{"cli with model", Backend{Type: BackendCLI, Command: []string{"x"}, Model: "m"}, "must not set model"},
		{"api ok", Backend{Type: BackendAnthropicAPI, Model: "claude"}, ""},
		{"api no model", Backend{Type: BackendAnthropicAPI}, "requires model"},
		{"api with command", Backend{Type: BackendAnthropicAPI, Model: "claude", Command: []string{"x"}}, "must not set command"},
		{"unknown type", Backend{Type: "foo"}, "invalid type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.b.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestExecutionValidate(t *testing.T) {
	tests := []struct {
		name    string
		e       Execution
		wantErr string
	}{
		{"on-demand ok", Execution{Mode: ExecutionOnDemand, Pool: "cli"}, ""},
		{"service ok", Execution{Mode: ExecutionService, Pool: "cli"}, ""},
		{"bad mode", Execution{Mode: "foo", Pool: "cli"}, "invalid mode"},
		{"empty pool", Execution{Mode: ExecutionOnDemand, Pool: ""}, "pool"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.e.Validate()
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

func TestConversationValidate(t *testing.T) {
	tests := []struct {
		name    string
		c       Conversation
		wantErr string
	}{
		{"stateless no key", Conversation{Mode: ConversationStateless}, ""},
		{"stateless with key", Conversation{Mode: ConversationStateless, Key: "x"}, "must not set key"},
		{"stateful no key", Conversation{Mode: ConversationStateful}, "requires key"},
		{"stateful with key", Conversation{Mode: ConversationStateful, Key: "x"}, ""},
		{"bad mode", Conversation{Mode: "foo"}, "invalid mode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
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

func TestTriggerValidate(t *testing.T) {
	tests := []struct {
		name    string
		tr      Trigger
		wantErr string
	}{
		{"cron ok", Trigger{Type: TriggerCron, Schedule: "* * * * *"}, ""},
		{"cron no schedule", Trigger{Type: TriggerCron}, "requires schedule"},
		{"cron with route", Trigger{Type: TriggerCron, Schedule: "* * * * *", Route: "/x"}, "must not set route"},
		{"webhook no route", Trigger{Type: TriggerWebhook}, "starting with"},
		{"webhook bad route", Trigger{Type: TriggerWebhook, Route: "foo"}, "starting with"},
		{"webhook ok", Trigger{Type: TriggerWebhook, Route: "/foo"}, ""},
		{"webhook with schedule", Trigger{Type: TriggerWebhook, Route: "/x", Schedule: "* * * * *"}, "must not set schedule"},
		{"bad type", Trigger{Type: "foo"}, "invalid type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tr.Validate()
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

func TestToolValidate(t *testing.T) {
	tests := []struct {
		name    string
		tl      Tool
		wantErr string
	}{
		{"name starts with digit", Tool{Name: "1foo", Protocol: ToolExec, Command: []string{"x"}}, "name"},
		{"name uppercase", Tool{Name: "Foo", Protocol: ToolExec, Command: []string{"x"}}, "name"},
		{"exec no command", Tool{Name: "x", Protocol: ToolExec}, "requires non-empty command"},
		{"exec with method", Tool{Name: "x", Protocol: ToolExec, Command: []string{"y"}, Method: "GET"}, "must not set http fields"},
		{"exec ok", Tool{Name: "x", Protocol: ToolExec, Command: []string{"y"}}, ""},
		{"http no method", Tool{Name: "x", Protocol: ToolHTTP, URL: "https://a"}, "method in"},
		{"http bad method", Tool{Name: "x", Protocol: ToolHTTP, Method: "OPTIONS", URL: "https://a"}, "method in"},
		{"http no url", Tool{Name: "x", Protocol: ToolHTTP, Method: "GET"}, "requires url"},
		{"http ok", Tool{Name: "x", Protocol: ToolHTTP, Method: "GET", URL: "https://a"}, ""},
		{"http with command", Tool{Name: "x", Protocol: ToolHTTP, Method: "GET", URL: "https://a", Command: []string{"y"}}, "must not set command"},
		{"bad protocol", Tool{Name: "x", Protocol: "foo"}, "invalid protocol"},
		{"schema invalid", Tool{Name: "x", Protocol: ToolExec, Command: []string{"y"}, InputSchema: map[string]string{"f": "date"}}, "invalid type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tl.Validate()
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

func TestToolValidate_AllSchemaTypes(t *testing.T) {
	for _, typ := range []string{"string", "number", "boolean", "object", "array"} {
		tl := Tool{Name: "x", Protocol: ToolExec, Command: []string{"y"}, InputSchema: map[string]string{"f": typ}}
		if err := tl.Validate(); err != nil {
			t.Fatalf("type %s unexpected err: %v", typ, err)
		}
	}
}
