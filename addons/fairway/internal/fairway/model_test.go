package fairway

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	validRoute := Route{
		Path: "/hooks/github",
		Auth: Auth{Type: AuthBearer, Token: "secret"},
		Action: Action{
			Type:   ActionCronRun,
			Target: "job-1",
		},
	}

	tests := []struct {
		name   string
		config Config
		errIs  error
	}{
		{
			name: "validMinimal",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          DefaultPort,
				Bind:          DefaultBind,
			},
		},
		{
			name: "validWithMultipleRoutes",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          9877,
				Bind:          "::1",
				MaxInFlight:   2,
				Routes: []Route{
					validRoute,
					{
						Path: "/hooks/grafana",
						Auth: Auth{Type: AuthToken, Value: "v", Header: "X-Token"},
						Action: Action{
							Type: ActionHTTPForward,
							URL:  "https://example.com/webhook",
						},
					},
				},
			},
		},
		{
			name: "invalidSchemaVersionEmpty",
			config: Config{
				SchemaVersion: "",
				Port:          DefaultPort,
				Bind:          DefaultBind,
			},
			errIs: ErrUnsupportedSchema,
		},
		{
			name: "invalidSchemaVersionV2",
			config: Config{
				SchemaVersion: "2",
				Port:          DefaultPort,
				Bind:          DefaultBind,
			},
			errIs: ErrUnsupportedSchema,
		},
		{
			name: "invalidSchemaVersionLegacyName",
			config: Config{
				SchemaVersion: "v1",
				Port:          DefaultPort,
				Bind:          DefaultBind,
			},
			errIs: ErrUnsupportedSchema,
		},
		{
			name: "invalidPortZero",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          0,
				Bind:          DefaultBind,
			},
			errIs: ErrInvalidPort,
		},
		{
			name: "invalidPortNegative",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          -1,
				Bind:          DefaultBind,
			},
			errIs: ErrInvalidPort,
		},
		{
			name: "invalidPortTooHigh",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          65536,
				Bind:          DefaultBind,
			},
			errIs: ErrInvalidPort,
		},
		{
			name: "invalidBindNotIPLocalhost",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          DefaultPort,
				Bind:          "localhost",
			},
			errIs: ErrInvalidBind,
		},
		{
			name: "invalidBindNotIPZero",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          DefaultPort,
				Bind:          "0",
			},
			errIs: ErrInvalidBind,
		},
		{
			name: "invalidBindEmpty",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          DefaultPort,
				Bind:          "",
			},
			errIs: ErrInvalidBind,
		},
		{
			name: "invalidMaxInFlightNegative",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          DefaultPort,
				Bind:          DefaultBind,
				MaxInFlight:   -1,
			},
			errIs: ErrInvalidMaxInFlight,
		},
		{
			name: "duplicateRoutePaths",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          DefaultPort,
				Bind:          DefaultBind,
				Routes: []Route{
					validRoute,
					validRoute,
				},
			},
			errIs: ErrDuplicateRoutePath,
		},
		{
			name: "invalidRouteWrapped",
			config: Config{
				SchemaVersion: SchemaVersion,
				Port:          DefaultPort,
				Bind:          DefaultBind,
				Routes: []Route{
					{
						Path:   "/hooks/github",
						Auth:   Auth{Type: AuthBearer},
						Action: Action{Type: ActionCronRun, Target: "job-1"},
					},
				},
			},
			errIs: ErrMissingAuthToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.errIs == nil {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tt.errIs) {
				t.Fatalf("Validate() error = %v, want errors.Is(..., %v)", err, tt.errIs)
			}
		})
	}
}

func TestRouteValidate(t *testing.T) {
	tests := []struct {
		name  string
		route Route
		errIs error
	}{
		{
			name: "validSimpleRoute",
			route: Route{
				Path:    "/hooks/github",
				Timeout: time.Second,
				Auth:    Auth{Type: AuthBearer, Token: "secret"},
				Action:  Action{Type: ActionCronRun, Target: "job-1"},
			},
		},
		{
			name: "invalidPathMissingSlash",
			route: Route{
				Path:   "hooks/github",
				Auth:   Auth{Type: AuthBearer, Token: "secret"},
				Action: Action{Type: ActionCronRun, Target: "job-1"},
			},
			errIs: ErrInvalidRoutePath,
		},
		{
			name: "invalidPathWithSpace",
			route: Route{
				Path:   "/hooks/git hub",
				Auth:   Auth{Type: AuthBearer, Token: "secret"},
				Action: Action{Type: ActionCronRun, Target: "job-1"},
			},
			errIs: ErrInvalidRoutePath,
		},
		{
			name: "invalidPathWithWildcard",
			route: Route{
				Path:   "/hooks/*",
				Auth:   Auth{Type: AuthBearer, Token: "secret"},
				Action: Action{Type: ActionCronRun, Target: "job-1"},
			},
			errIs: ErrInvalidRoutePath,
		},
		{
			name: "invalidTimeoutNegative",
			route: Route{
				Path:    "/hooks/github",
				Timeout: -1 * time.Second,
				Auth:    Auth{Type: AuthBearer, Token: "secret"},
				Action:  Action{Type: ActionCronRun, Target: "job-1"},
			},
			errIs: ErrInvalidRouteTimeout,
		},
		{
			name: "invalidTimeoutTooLong",
			route: Route{
				Path:    "/hooks/github",
				Timeout: 6 * time.Minute,
				Auth:    Auth{Type: AuthBearer, Token: "secret"},
				Action:  Action{Type: ActionCronRun, Target: "job-1"},
			},
			errIs: ErrInvalidRouteTimeout,
		},
		{
			name: "invalidAuthWrapped",
			route: Route{
				Path:   "/hooks/github",
				Auth:   Auth{Type: AuthBearer},
				Action: Action{Type: ActionCronRun, Target: "job-1"},
			},
			errIs: ErrMissingAuthToken,
		},
		{
			name: "invalidActionWrapped",
			route: Route{
				Path:   "/hooks/github",
				Auth:   Auth{Type: AuthBearer, Token: "secret"},
				Action: Action{Type: ActionCronRun},
			},
			errIs: ErrMissingActionTarget,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.route.Validate()
			if tt.errIs == nil {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tt.errIs) {
				t.Fatalf("Validate() error = %v, want errors.Is(..., %v)", err, tt.errIs)
			}
		})
	}
}

func TestAuthValidate(t *testing.T) {
	tests := []struct {
		name  string
		auth  Auth
		errIs error
	}{
		{name: "bearerValid", auth: Auth{Type: AuthBearer, Token: "secret"}},
		{name: "bearerMissingToken", auth: Auth{Type: AuthBearer}, errIs: ErrMissingAuthToken},
		{name: "bearerUnexpectedField", auth: Auth{Type: AuthBearer, Token: "secret", Header: "X-Token"}, errIs: ErrUnexpectedAuthField},
		{name: "tokenValidHeader", auth: Auth{Type: AuthToken, Value: "secret", Header: "X-Token"}},
		{name: "tokenValidQuery", auth: Auth{Type: AuthToken, Value: "secret", Query: "token"}},
		{name: "tokenMissingValue", auth: Auth{Type: AuthToken, Header: "X-Token"}, errIs: ErrMissingAuthValue},
		{name: "tokenMissingLocation", auth: Auth{Type: AuthToken, Value: "secret"}, errIs: ErrMissingAuthLocation},
		{name: "tokenUnexpectedTokenField", auth: Auth{Type: AuthToken, Value: "secret", Header: "X-Token", Token: "nope"}, errIs: ErrUnexpectedAuthField},
		{name: "localOnlyValid", auth: Auth{Type: AuthLocalOnly}},
		{name: "localOnlyUnexpectedField", auth: Auth{Type: AuthLocalOnly, Value: "secret"}, errIs: ErrUnexpectedAuthField},
		{name: "invalidAuthType", auth: Auth{Type: AuthType("mtls")}, errIs: ErrInvalidAuthType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.auth.Validate()
			if tt.errIs == nil {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tt.errIs) {
				t.Fatalf("Validate() error = %v, want errors.Is(..., %v)", err, tt.errIs)
			}
		})
	}
}

func TestActionValidate(t *testing.T) {
	tests := []struct {
		name   string
		action Action
		errIs  error
	}{
		{name: "cronRunValid", action: Action{Type: ActionCronRun, Target: "job-1"}},
		{name: "cronRunMissingTarget", action: Action{Type: ActionCronRun}, errIs: ErrMissingActionTarget},
		{name: "cronEnableValid", action: Action{Type: ActionCronEnable, Target: "job-1"}},
		{name: "cronEnableMissingTarget", action: Action{Type: ActionCronEnable}, errIs: ErrMissingActionTarget},
		{name: "cronDisableValid", action: Action{Type: ActionCronDisable, Target: "job-1"}},
		{name: "cronDisableMissingTarget", action: Action{Type: ActionCronDisable}, errIs: ErrMissingActionTarget},
		{name: "serviceStartValid", action: Action{Type: ActionServiceStart, Target: "svc-1"}},
		{name: "serviceStartMissingTarget", action: Action{Type: ActionServiceStart}, errIs: ErrMissingActionTarget},
		{name: "serviceStopValid", action: Action{Type: ActionServiceStop, Target: "svc-1"}},
		{name: "serviceStopMissingTarget", action: Action{Type: ActionServiceStop}, errIs: ErrMissingActionTarget},
		{name: "serviceRestartValid", action: Action{Type: ActionServiceRestart, Target: "svc-1"}},
		{name: "serviceRestartMissingTarget", action: Action{Type: ActionServiceRestart}, errIs: ErrMissingActionTarget},
		{name: "messageSendValid", action: Action{Type: ActionMessageSend, Provider: "telegram"}},
		{name: "telegramHandleValid", action: Action{Type: ActionTelegramHandle}},
		{name: "httpForwardValid", action: Action{Type: ActionHTTPForward, URL: "https://example.com/hook"}},
		{name: "httpForwardInvalidScheme", action: Action{Type: ActionHTTPForward, URL: "file:///tmp/a"}, errIs: ErrInvalidActionURL},
		{name: "httpForwardParseError", action: Action{Type: ActionHTTPForward, URL: "http://[::1"}, errIs: ErrInvalidActionURL},
		{name: "httpForwardMissingURL", action: Action{Type: ActionHTTPForward}, errIs: ErrInvalidActionURL},
		{name: "httpForwardMissingHost", action: Action{Type: ActionHTTPForward, URL: "https:///path"}, errIs: ErrInvalidActionURL},
		{name: "invalidActionType", action: Action{Type: ActionType("logs.query")}, errIs: ErrInvalidActionType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.action.Validate()
			if tt.errIs == nil {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tt.errIs) {
				t.Fatalf("Validate() error = %v, want errors.Is(..., %v)", err, tt.errIs)
			}
		})
	}
}

func TestConfigMarshalStable(t *testing.T) {
	cfg := Config{
		SchemaVersion: SchemaVersion,
		Port:          DefaultPort,
		Bind:          DefaultBind,
		MaxInFlight:   DefaultMaxInFlight,
		Routes: []Route{
			{
				Path:    "/hooks/github",
				Timeout: 10 * time.Second,
				Auth:    Auth{Type: AuthBearer, Token: "secret"},
				Action:  Action{Type: ActionCronRun, Target: "job-1"},
			},
			{
				Path: "/hooks/forward",
				Auth: Auth{Type: AuthToken, Value: "secret", Header: "X-Token"},
				Action: Action{
					Type:    ActionHTTPForward,
					URL:     "https://example.com/endpoint",
					Method:  "POST",
					Headers: map[string]string{"X-Test": "1", "X-Alpha": "2"},
				},
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if !reflect.DeepEqual(decoded, cfg) {
		t.Fatalf("round-trip mismatch:\n got: %#v\nwant: %#v", decoded, cfg)
	}
}

func TestConfigMarshalOmitsEmpty(t *testing.T) {
	cfg := Config{
		SchemaVersion: SchemaVersion,
		Port:          DefaultPort,
		Bind:          DefaultBind,
		Routes: []Route{
			{
				Path:   "/hooks/github",
				Auth:   Auth{Type: AuthLocalOnly},
				Action: Action{Type: ActionTelegramHandle},
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	text := string(data)
	for _, fragment := range []string{
		`"maxInFlight"`,
		`"timeout"`,
		`"token"`,
		`"value"`,
		`"header"`,
		`"query"`,
		`"target"`,
		`"provider"`,
		`"url"`,
		`"method"`,
		`"headers"`,
	} {
		if strings.Contains(text, fragment) {
			t.Fatalf("Marshal() output unexpectedly contains %s: %s", fragment, text)
		}
	}
}

func TestConfigUnmarshalMalformedJSON(t *testing.T) {
	var cfg Config
	err := json.Unmarshal([]byte(`{"schemaVersion":`), &cfg)
	if err == nil {
		t.Fatal("Unmarshal() error = nil, want error")
	}
	match := regexp.MustCompile(`(?i)(unexpected end of JSON input|unexpected EOF)`)
	if !match.MatchString(err.Error()) {
		t.Fatalf("Unmarshal() error = %q, want malformed JSON error", err.Error())
	}
}
