package fairway_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func validConfig() fairway.Config {
	return fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          9876,
		Bind:          "127.0.0.1",
		Routes:        nil,
	}
}

func validRoute() fairway.Route {
	return fairway.Route{
		Path: "/webhook",
		Auth: fairway.Auth{
			Type:  fairway.AuthBearer,
			Token: "secret",
		},
		Action: fairway.Action{
			Type:   fairway.ActionCronRun,
			Target: "my-job",
		},
	}
}

// ── Config.Validate ───────────────────────────────────────────────────────────

func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*fairway.Config)
		wantErr error
	}{
		{
			name:    "validMinimal",
			mutate:  nil,
			wantErr: nil,
		},
		{
			name: "validWithMultipleRoutes",
			mutate: func(c *fairway.Config) {
				c.Routes = []fairway.Route{
					{Path: "/a", Auth: fairway.Auth{Type: fairway.AuthLocalOnly}, Action: fairway.Action{Type: fairway.ActionCronRun, Target: "j1"}},
					{Path: "/b", Auth: fairway.Auth{Type: fairway.AuthLocalOnly}, Action: fairway.Action{Type: fairway.ActionCronRun, Target: "j2"}},
				}
			},
			wantErr: nil,
		},
		{
			name:    "invalidSchemaVersionWrong",
			mutate:  func(c *fairway.Config) { c.SchemaVersion = "2" },
			wantErr: fairway.ErrUnsupportedSchema,
		},
		{
			name:    "invalidSchemaVersionEmpty",
			mutate:  func(c *fairway.Config) { c.SchemaVersion = "" },
			wantErr: fairway.ErrUnsupportedSchema,
		},
		{
			name:    "invalidSchemaVersionPrefix",
			mutate:  func(c *fairway.Config) { c.SchemaVersion = "v1" },
			wantErr: fairway.ErrUnsupportedSchema,
		},
		{
			name:    "invalidPortZero",
			mutate:  func(c *fairway.Config) { c.Port = 0 },
			wantErr: fairway.ErrInvalidPort,
		},
		{
			name:    "invalidPortNegative",
			mutate:  func(c *fairway.Config) { c.Port = -1 },
			wantErr: fairway.ErrInvalidPort,
		},
		{
			name:    "invalidPortTooHigh",
			mutate:  func(c *fairway.Config) { c.Port = 65536 },
			wantErr: fairway.ErrInvalidPort,
		},
		{
			name:    "validPortBoundaryLow",
			mutate:  func(c *fairway.Config) { c.Port = 1 },
			wantErr: nil,
		},
		{
			name:    "validPortBoundaryHigh",
			mutate:  func(c *fairway.Config) { c.Port = 65535 },
			wantErr: nil,
		},
		{
			name:    "invalidBindNotIP_hostname",
			mutate:  func(c *fairway.Config) { c.Bind = "localhost" },
			wantErr: fairway.ErrInvalidBind,
		},
		{
			name:    "invalidBindNotIP_zero",
			mutate:  func(c *fairway.Config) { c.Bind = "0" },
			wantErr: fairway.ErrInvalidBind,
		},
		{
			name:    "invalidBindNotIP_empty",
			mutate:  func(c *fairway.Config) { c.Bind = "" },
			wantErr: fairway.ErrInvalidBind,
		},
		{
			name:    "validBindIPv6",
			mutate:  func(c *fairway.Config) { c.Bind = "::1" },
			wantErr: nil,
		},
		{
			name:    "validBindAllInterfaces",
			mutate:  func(c *fairway.Config) { c.Bind = "0.0.0.0" },
			wantErr: nil,
		},
		{
			name:    "invalidMaxInFlightNegative",
			mutate:  func(c *fairway.Config) { c.MaxInFlight = -1 },
			wantErr: fairway.ErrInvalidMaxInFlight,
		},
		{
			name:    "validMaxInFlightZeroMeansDefault",
			mutate:  func(c *fairway.Config) { c.MaxInFlight = 0 },
			wantErr: nil,
		},
		{
			name: "duplicateRoutePaths",
			mutate: func(c *fairway.Config) {
				r := fairway.Route{Path: "/dup", Auth: fairway.Auth{Type: fairway.AuthLocalOnly}, Action: fairway.Action{Type: fairway.ActionCronRun, Target: "j"}}
				c.Routes = []fairway.Route{r, r}
			},
			wantErr: fairway.ErrDuplicateRoutePath,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			err := cfg.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v; want errors.Is(%v)", err, tc.wantErr)
			}
		})
	}
}

// ── Route.Validate ────────────────────────────────────────────────────────────

func TestRoute_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*fairway.Route)
		wantErr error
	}{
		{
			name:    "validSimpleRoute",
			mutate:  nil,
			wantErr: nil,
		},
		{
			name:    "invalidPathMissingSlash",
			mutate:  func(r *fairway.Route) { r.Path = "no-slash" },
			wantErr: fairway.ErrInvalidPath,
		},
		{
			name:    "invalidPathWithSpace",
			mutate:  func(r *fairway.Route) { r.Path = "/has space" },
			wantErr: fairway.ErrInvalidPath,
		},
		{
			name:    "invalidPathWithWildcard",
			mutate:  func(r *fairway.Route) { r.Path = "/wild*card" },
			wantErr: fairway.ErrInvalidPath,
		},
		{
			name:    "invalidPathWithQuestionMark",
			mutate:  func(r *fairway.Route) { r.Path = "/path?q=1" },
			wantErr: fairway.ErrInvalidPath,
		},
		{
			name:    "invalidPathWithHash",
			mutate:  func(r *fairway.Route) { r.Path = "/path#anchor" },
			wantErr: fairway.ErrInvalidPath,
		},
		{
			name:    "invalidTimeoutNegative",
			mutate:  func(r *fairway.Route) { r.Timeout = -1 * time.Second },
			wantErr: fairway.ErrInvalidTimeout,
		},
		{
			name:    "invalidTimeoutTooLong",
			mutate:  func(r *fairway.Route) { r.Timeout = 6 * time.Minute },
			wantErr: fairway.ErrInvalidTimeout,
		},
		{
			name:    "validTimeoutAtMax",
			mutate:  func(r *fairway.Route) { r.Timeout = 5 * time.Minute },
			wantErr: nil,
		},
		{
			name:    "validTimeoutZeroMeansDefault",
			mutate:  func(r *fairway.Route) { r.Timeout = 0 },
			wantErr: nil,
		},
		{
			name: "asyncWithCrewRunAllowed",
			mutate: func(r *fairway.Route) {
				r.Async = true
				r.Action = fairway.Action{Type: fairway.ActionCrewRun, Target: "agent-x"}
			},
			wantErr: nil,
		},
		{
			name: "asyncWithCronRunAllowed",
			mutate: func(r *fairway.Route) {
				r.Async = true
				r.Action = fairway.Action{Type: fairway.ActionCronRun, Target: "job-1"}
			},
			wantErr: nil,
		},
		{
			name: "asyncWithHTTPForwardRejected",
			mutate: func(r *fairway.Route) {
				r.Async = true
				r.Action = fairway.Action{Type: fairway.ActionHTTPForward, URL: "https://example.com"}
			},
			wantErr: fairway.ErrInvalidAsyncForward,
		},
		{
			name: "syncWithHTTPForwardAllowed",
			mutate: func(r *fairway.Route) {
				r.Async = false
				r.Action = fairway.Action{Type: fairway.ActionHTTPForward, URL: "https://example.com"}
			},
			wantErr: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := validRoute()
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			err := r.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Route.Validate() unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Route.Validate() = %v; want errors.Is(%v)", err, tc.wantErr)
			}
		})
	}
}

// ── Auth.Validate ─────────────────────────────────────────────────────────────

func TestAuth_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		auth    fairway.Auth
		wantErr error
	}{
		// bearer
		{
			name:    "bearerValid",
			auth:    fairway.Auth{Type: fairway.AuthBearer, Token: "tok"},
			wantErr: nil,
		},
		{
			name:    "bearerMissingToken",
			auth:    fairway.Auth{Type: fairway.AuthBearer, Token: ""},
			wantErr: fairway.ErrMissingAuthToken,
		},
		// token
		{
			name:    "tokenValidWithHeader",
			auth:    fairway.Auth{Type: fairway.AuthToken, Value: "v", Header: "X-Token"},
			wantErr: nil,
		},
		{
			name:    "tokenValidWithQuery",
			auth:    fairway.Auth{Type: fairway.AuthToken, Value: "v", Query: "token"},
			wantErr: nil,
		},
		{
			name:    "tokenMissingValue",
			auth:    fairway.Auth{Type: fairway.AuthToken, Value: "", Header: "X-Token"},
			wantErr: fairway.ErrMissingAuthValue,
		},
		{
			name:    "tokenMissingLookup",
			auth:    fairway.Auth{Type: fairway.AuthToken, Value: "v"},
			wantErr: fairway.ErrMissingAuthLookup,
		},
		// local-only
		{
			name:    "localOnlyValid",
			auth:    fairway.Auth{Type: fairway.AuthLocalOnly},
			wantErr: nil,
		},
		{
			name:    "localOnlyWithToken",
			auth:    fairway.Auth{Type: fairway.AuthLocalOnly, Token: "x"},
			wantErr: fairway.ErrLocalOnlyExtraField,
		},
		{
			name:    "localOnlyWithValue",
			auth:    fairway.Auth{Type: fairway.AuthLocalOnly, Value: "x"},
			wantErr: fairway.ErrLocalOnlyExtraField,
		},
		{
			name:    "localOnlyWithHeader",
			auth:    fairway.Auth{Type: fairway.AuthLocalOnly, Header: "X-H"},
			wantErr: fairway.ErrLocalOnlyExtraField,
		},
		{
			name:    "localOnlyWithQuery",
			auth:    fairway.Auth{Type: fairway.AuthLocalOnly, Query: "q"},
			wantErr: fairway.ErrLocalOnlyExtraField,
		},
		// unknown
		{
			name:    "unknownType",
			auth:    fairway.Auth{Type: "magic"},
			wantErr: fairway.ErrInvalidAuthType,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.auth.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Auth.Validate() unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Auth.Validate() = %v; want errors.Is(%v)", err, tc.wantErr)
			}
		})
	}
}

// ── Action.Validate ───────────────────────────────────────────────────────────

func TestAction_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		action  fairway.Action
		wantErr error
	}{
		// cron
		{name: "cronRunValid", action: fairway.Action{Type: fairway.ActionCronRun, Target: "job-1"}, wantErr: nil},
		{name: "cronRunMissingTarget", action: fairway.Action{Type: fairway.ActionCronRun}, wantErr: fairway.ErrMissingActionTarget},
		{name: "cronEnableValid", action: fairway.Action{Type: fairway.ActionCronEnable, Target: "job-1"}, wantErr: nil},
		{name: "cronEnableMissingTarget", action: fairway.Action{Type: fairway.ActionCronEnable}, wantErr: fairway.ErrMissingActionTarget},
		{name: "cronDisableValid", action: fairway.Action{Type: fairway.ActionCronDisable, Target: "job-1"}, wantErr: nil},
		{name: "cronDisableMissingTarget", action: fairway.Action{Type: fairway.ActionCronDisable}, wantErr: fairway.ErrMissingActionTarget},
		// service
		{name: "serviceStartValid", action: fairway.Action{Type: fairway.ActionServiceStart, Target: "svc-1"}, wantErr: nil},
		{name: "serviceStartMissingTarget", action: fairway.Action{Type: fairway.ActionServiceStart}, wantErr: fairway.ErrMissingActionTarget},
		{name: "serviceStopValid", action: fairway.Action{Type: fairway.ActionServiceStop, Target: "svc-1"}, wantErr: nil},
		{name: "serviceStopMissingTarget", action: fairway.Action{Type: fairway.ActionServiceStop}, wantErr: fairway.ErrMissingActionTarget},
		{name: "serviceRestartValid", action: fairway.Action{Type: fairway.ActionServiceRestart, Target: "svc-1"}, wantErr: nil},
		{name: "serviceRestartMissingTarget", action: fairway.Action{Type: fairway.ActionServiceRestart}, wantErr: fairway.ErrMissingActionTarget},
		// crew
		{name: "crewRunValid", action: fairway.Action{Type: fairway.ActionCrewRun, Target: "promo-hunter"}, wantErr: nil},
		{name: "crewRunMissingTarget", action: fairway.Action{Type: fairway.ActionCrewRun}, wantErr: fairway.ErrMissingActionTarget},
		// message/telegram
		{name: "messageSendValid", action: fairway.Action{Type: fairway.ActionMessageSend}, wantErr: nil},
		{name: "messageSendWithProvider", action: fairway.Action{Type: fairway.ActionMessageSend, Provider: "slack"}, wantErr: nil},
		{name: "telegramHandleValid", action: fairway.Action{Type: fairway.ActionTelegramHandle}, wantErr: nil},
		// http.forward
		{name: "httpForwardValid", action: fairway.Action{Type: fairway.ActionHTTPForward, URL: "https://example.com/hook"}, wantErr: nil},
		{name: "httpForwardHTTP", action: fairway.Action{Type: fairway.ActionHTTPForward, URL: "http://example.com"}, wantErr: nil},
		{name: "httpForwardMissingURL", action: fairway.Action{Type: fairway.ActionHTTPForward, URL: ""}, wantErr: fairway.ErrInvalidActionURL},
		{name: "httpForwardInvalidScheme", action: fairway.Action{Type: fairway.ActionHTTPForward, URL: "file:///etc/passwd"}, wantErr: fairway.ErrInvalidActionURL},
		{name: "httpForwardFTPScheme", action: fairway.Action{Type: fairway.ActionHTTPForward, URL: "ftp://example.com"}, wantErr: fairway.ErrInvalidActionURL},
		// unknown
		{name: "unknownActionType", action: fairway.Action{Type: "magic.do"}, wantErr: fairway.ErrInvalidActionType},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.action.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Action.Validate() unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Action.Validate() = %v; want errors.Is(%v)", err, tc.wantErr)
			}
		})
	}
}

// ── Marshaling ────────────────────────────────────────────────────────────────

func TestConfig_MarshalStable(t *testing.T) {
	t.Parallel()

	original := fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          9876,
		Bind:          "127.0.0.1",
		MaxInFlight:   16,
		Routes: []fairway.Route{
			{
				Path: "/webhook",
				Auth: fairway.Auth{
					Type:  fairway.AuthBearer,
					Token: "supersecret",
				},
				Action: fairway.Action{
					Type:   fairway.ActionCronRun,
					Target: "nightly-backup",
				},
				Timeout: 10 * time.Second,
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}

	var decoded fairway.Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	// Deep compare via re-marshal.
	data2, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("json.Marshal(decoded) error: %v", err)
	}
	if string(data) != string(data2) {
		t.Fatalf("marshal not stable:\n  first:  %s\n  second: %s", data, data2)
	}
}

func TestConfig_MarshalStable_async(t *testing.T) {
	t.Parallel()

	original := fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          9876,
		Bind:          "127.0.0.1",
		MaxInFlight:   16,
		Routes: []fairway.Route{
			{
				Path:   "/agent",
				Auth:   fairway.Auth{Type: fairway.AuthLocalOnly},
				Action: fairway.Action{Type: fairway.ActionCrewRun, Target: "agent-x"},
				Async:  true,
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	if !strings.Contains(string(data), `"async":true`) {
		t.Fatalf(`marshaled Config must contain "async":true; got: %s`, data)
	}

	var decoded fairway.Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if !decoded.Routes[0].Async {
		t.Fatalf("round-trip lost Async=true")
	}

	data2, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("json.Marshal(decoded) error: %v", err)
	}
	if string(data) != string(data2) {
		t.Fatalf("marshal not stable:\n  first:  %s\n  second: %s", data, data2)
	}
}

func TestConfig_MarshalOmitsEmpty(t *testing.T) {
	t.Parallel()

	cfg := fairway.Config{
		SchemaVersion: fairway.SchemaVersion,
		Port:          9876,
		Bind:          "127.0.0.1",
		// MaxInFlight zero → omitempty
		Routes: []fairway.Route{
			{
				Path: "/ping",
				Auth: fairway.Auth{
					Type: fairway.AuthLocalOnly,
					// Token/Value/Header/Query all empty → omitted
				},
				Action: fairway.Action{
					Type: fairway.ActionMessageSend,
					// Target/Provider/URL/Method/Headers all empty → omitted
				},
				// Timeout zero → omitted
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}

	s := string(data)
	for _, forbidden := range []string{`"maxInFlight"`, `"token"`, `"value"`, `"header"`, `"query"`, `"target"`, `"provider"`, `"url"`, `"method"`, `"headers"`, `"timeout"`, `"async"`} {
		if strings.Contains(s, forbidden) {
			t.Errorf("expected %q to be omitted but found it in: %s", forbidden, s)
		}
	}
}

func TestConfig_UnmarshalMalformed(t *testing.T) {
	t.Parallel()

	bad := []string{
		`{`,
		`{"port": "not-a-number"}`,
		`null`,
		`[]`,
	}

	for _, input := range bad {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			var cfg fairway.Config
			err := json.Unmarshal([]byte(input), &cfg)
			// null and [] decode without error in Go (zero value) — acceptable.
			// Only check that truly malformed JSON returns error.
			if input == `{` && err == nil {
				t.Fatalf("expected error for malformed JSON %q but got nil", input)
			}
			if input == `{"port": "not-a-number"}` && err == nil {
				t.Fatalf("expected type error for %q but got nil", input)
			}
		})
	}
}
