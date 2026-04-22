// Package fairway defines the domain types for the shipyard-fairway daemon.
// These types are the backbone consumed by the router, auth, executor and socket layers.
package fairway

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Schema and runtime constants.
const (
	// SchemaVersion is the only accepted value for Config.SchemaVersion.
	SchemaVersion = "1"

	// DefaultPort is the TCP port the HTTP server listens on when none is configured.
	DefaultPort = 9876

	// DefaultBind is the default interface address for the HTTP server.
	DefaultBind = "127.0.0.1"

	// DefaultMaxInFlight is the maximum number of concurrent subprocess executions.
	DefaultMaxInFlight = 16

	// DefaultActionTimeout is the default per-action subprocess timeout.
	DefaultActionTimeout = 30 * time.Second

	// DefaultQueueTimeout is how long a request waits for a slot in the worker pool.
	DefaultQueueTimeout = 5 * time.Second

	// MaxSubprocessOutput caps the bytes read from a subprocess stdout+stderr.
	MaxSubprocessOutput = 4 * 1024 * 1024 // 4 MB
)

// Sentinel errors returned by Validate methods. Callers may use errors.Is.
var (
	ErrUnsupportedSchema   = errors.New("unsupported schema version")
	ErrInvalidPort         = errors.New("invalid port")
	ErrInvalidBind         = errors.New("invalid bind address")
	ErrInvalidMaxInFlight  = errors.New("maxInFlight must be greater than zero")
	ErrDuplicateRoutePath  = errors.New("duplicate route path")
	ErrInvalidPath         = errors.New("invalid route path")
	ErrInvalidTimeout      = errors.New("invalid timeout")
	ErrInvalidAuthType     = errors.New("invalid auth type")
	ErrMissingAuthToken    = errors.New("bearer auth requires a non-empty token")
	ErrMissingAuthValue    = errors.New("token auth requires a non-empty value")
	ErrMissingAuthLookup   = errors.New("token auth requires header or query to be set")
	ErrLocalOnlyExtraField = errors.New("local-only auth must not have token, value, header or query fields")
	ErrInvalidActionType   = errors.New("invalid action type")
	ErrMissingActionTarget = errors.New("action requires a non-empty target")
	ErrInvalidActionURL    = errors.New("http.forward requires a valid http/https URL")
)

// AuthType enumerates the supported authentication strategies for a route.
type AuthType string

const (
	// AuthBearer validates requests via "Authorization: Bearer <token>" header.
	AuthBearer AuthType = "bearer"

	// AuthToken validates requests via a configurable header or query parameter.
	AuthToken AuthType = "token"

	// AuthLocalOnly allows requests only from loopback addresses (127.x.x.x / ::1).
	AuthLocalOnly AuthType = "local-only"
)

// ActionType enumerates the actions fairway can dispatch when a route is hit.
type ActionType string

const (
	// ActionCronRun triggers an immediate run of a shipyard cron job.
	ActionCronRun ActionType = "cron.run"

	// ActionCronEnable enables a shipyard cron job.
	ActionCronEnable ActionType = "cron.enable"

	// ActionCronDisable disables a shipyard cron job.
	ActionCronDisable ActionType = "cron.disable"

	// ActionServiceStart starts a shipyard-managed service.
	ActionServiceStart ActionType = "service.start"

	// ActionServiceStop stops a shipyard-managed service.
	ActionServiceStop ActionType = "service.stop"

	// ActionServiceRestart restarts a shipyard-managed service.
	ActionServiceRestart ActionType = "service.restart"

	// ActionMessageSend sends a message via an optional provider.
	ActionMessageSend ActionType = "message.send"

	// ActionTelegramHandle triggers a Telegram handler.
	ActionTelegramHandle ActionType = "telegram.handle"

	// ActionHTTPForward proxies the incoming request to a downstream URL.
	ActionHTTPForward ActionType = "http.forward"

	// ActionCrewRun triggers an on-demand execution of a shipyard-crew agent.
	ActionCrewRun ActionType = "crew.run"
)

// Config is the top-level configuration for the fairway daemon, persisted in
// ~/.shipyard/fairway/config.json and loaded at startup.
type Config struct {
	// SchemaVersion must equal the SchemaVersion constant.
	SchemaVersion string `json:"schemaVersion"`

	// Port is the TCP port the HTTP server listens on.
	Port int `json:"port"`

	// Bind is the IP address the HTTP server binds to.
	Bind string `json:"bind"`

	// MaxInFlight limits concurrent subprocess executions. Defaults to DefaultMaxInFlight.
	MaxInFlight int `json:"maxInFlight,omitempty"`

	// Routes is the ordered list of route definitions.
	Routes []Route `json:"routes"`
}

// Validate checks all fields of Config and returns the first error encountered.
// Errors can be unwrapped with errors.Is against the sentinel variables.
func (c Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: got %q, want %q", ErrUnsupportedSchema, c.SchemaVersion, SchemaVersion)
	}

	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("%w: %d", ErrInvalidPort, c.Port)
	}

	if net.ParseIP(c.Bind) == nil {
		return fmt.Errorf("%w: %q", ErrInvalidBind, c.Bind)
	}

	if c.MaxInFlight < 0 || c.MaxInFlight == 0 && false {
		// zero means "use default", negative is invalid
	}
	if c.MaxInFlight < 0 {
		return fmt.Errorf("%w: %d", ErrInvalidMaxInFlight, c.MaxInFlight)
	}

	seen := make(map[string]struct{}, len(c.Routes))
	for i, r := range c.Routes {
		if _, dup := seen[r.Path]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateRoutePath, r.Path)
		}
		seen[r.Path] = struct{}{}
		if err := r.Validate(); err != nil {
			return fmt.Errorf("route[%d] %q: %w", i, r.Path, err)
		}
	}

	return nil
}

// Route describes a single HTTP endpoint and its corresponding action.
type Route struct {
	// Path is the HTTP path to match (must start with "/").
	Path string `json:"path"`

	// Auth defines the authentication strategy for this route.
	Auth Auth `json:"auth"`

	// Action defines what happens when this route is triggered.
	Action Action `json:"action"`

	// Timeout overrides DefaultActionTimeout for this route. Zero means use default.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Validate checks all fields of Route.
func (r Route) Validate() error {
	if !strings.HasPrefix(r.Path, "/") {
		return fmt.Errorf("%w: path must start with '/': %q", ErrInvalidPath, r.Path)
	}
	for _, ch := range []string{" ", "*", "?", "#"} {
		if strings.Contains(r.Path, ch) {
			return fmt.Errorf("%w: path contains forbidden character %q: %q", ErrInvalidPath, ch, r.Path)
		}
	}

	if r.Timeout < 0 {
		return fmt.Errorf("%w: must be >= 0, got %s", ErrInvalidTimeout, r.Timeout)
	}
	if r.Timeout > 5*time.Minute {
		return fmt.Errorf("%w: must be <= 5m, got %s", ErrInvalidTimeout, r.Timeout)
	}

	if err := r.Auth.Validate(); err != nil {
		return err
	}

	return r.Action.Validate()
}

// Auth holds the authentication configuration for a route.
type Auth struct {
	// Type is the authentication strategy.
	Type AuthType `json:"type"`

	// Token is the bearer token secret (used with AuthBearer).
	Token string `json:"token,omitempty"`

	// Value is the expected token value (used with AuthToken).
	Value string `json:"value,omitempty"`

	// Header is the request header name to look for the token (used with AuthToken).
	Header string `json:"header,omitempty"`

	// Query is the query parameter name to look for the token (used with AuthToken).
	Query string `json:"query,omitempty"`
}

// Validate checks the Auth configuration.
func (a Auth) Validate() error {
	switch a.Type {
	case AuthBearer:
		if a.Token == "" {
			return ErrMissingAuthToken
		}
	case AuthToken:
		if a.Value == "" {
			return ErrMissingAuthValue
		}
		if a.Header == "" && a.Query == "" {
			return ErrMissingAuthLookup
		}
	case AuthLocalOnly:
		if a.Token != "" || a.Value != "" || a.Header != "" || a.Query != "" {
			return ErrLocalOnlyExtraField
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidAuthType, a.Type)
	}
	return nil
}

// Action describes the operation to execute when a route is triggered.
type Action struct {
	// Type is the action category.
	Type ActionType `json:"type"`

	// Target is the job ID or service ID for cron/service actions.
	Target string `json:"target,omitempty"`

	// Provider is an optional message provider name (used with ActionMessageSend).
	Provider string `json:"provider,omitempty"`

	// URL is the downstream URL for http.forward actions.
	URL string `json:"url,omitempty"`

	// Method is the HTTP method to use for http.forward (default: same as incoming).
	Method string `json:"method,omitempty"`

	// Headers contains additional headers to inject for http.forward.
	Headers map[string]string `json:"headers,omitempty"`
}

// Validate checks the Action configuration.
func (a Action) Validate() error {
	switch a.Type {
	case ActionCronRun, ActionCronEnable, ActionCronDisable:
		if a.Target == "" {
			return fmt.Errorf("%w for action %q", ErrMissingActionTarget, a.Type)
		}
	case ActionServiceStart, ActionServiceStop, ActionServiceRestart:
		if a.Target == "" {
			return fmt.Errorf("%w for action %q", ErrMissingActionTarget, a.Type)
		}
	case ActionCrewRun:
		if a.Target == "" {
			return fmt.Errorf("%w for action %q", ErrMissingActionTarget, a.Type)
		}
	case ActionMessageSend, ActionTelegramHandle:
		// Provider is optional; no required fields.
	case ActionHTTPForward:
		if a.URL == "" {
			return fmt.Errorf("%w: URL is empty", ErrInvalidActionURL)
		}
		u, err := url.Parse(a.URL)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidActionURL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("%w: scheme must be http or https, got %q", ErrInvalidActionURL, u.Scheme)
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidActionType, a.Type)
	}
	return nil
}
