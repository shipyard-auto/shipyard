package fairway

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	// SchemaVersion is the supported routes/config schema for Fairway V1.
	SchemaVersion = "1"
	// DefaultPort is the default HTTP port used by the Fairway daemon.
	DefaultPort = 9876
	// DefaultBind is the default loopback bind address used by the Fairway daemon.
	DefaultBind = "127.0.0.1"
	// DefaultMaxInFlight limits concurrent subprocess actions.
	DefaultMaxInFlight = 16
	// DefaultActionTimeout bounds how long a route action may run.
	DefaultActionTimeout = 30 * time.Second
	// DefaultQueueTimeout bounds how long a request may wait for a subprocess slot.
	DefaultQueueTimeout = 5 * time.Second
	// MaxSubprocessOutput caps combined stdout/stderr captured from subprocess actions.
	MaxSubprocessOutput = 4 * 1024 * 1024
	maxRouteTimeout     = 5 * time.Minute
)

var (
	// ErrUnsupportedSchema indicates that the provided config schema version is not supported.
	ErrUnsupportedSchema = errors.New("unsupported schema version")
	// ErrInvalidPort indicates that the configured port is outside the valid TCP range.
	ErrInvalidPort = errors.New("invalid port")
	// ErrInvalidBind indicates that the configured bind address is not a valid IP literal.
	ErrInvalidBind = errors.New("invalid bind")
	// ErrInvalidMaxInFlight indicates that max in-flight concurrency is invalid.
	ErrInvalidMaxInFlight = errors.New("invalid max in flight")
	// ErrDuplicateRoutePath indicates that two or more routes share the same path.
	ErrDuplicateRoutePath = errors.New("duplicate route path")
	// ErrInvalidRoutePath indicates that a route path is empty or uses unsupported characters.
	ErrInvalidRoutePath = errors.New("invalid route path")
	// ErrInvalidRouteTimeout indicates that a route timeout is zero, negative, or above the supported bound.
	ErrInvalidRouteTimeout = errors.New("invalid route timeout")
	// ErrInvalidAuthType indicates that an auth type is unknown.
	ErrInvalidAuthType = errors.New("invalid auth type")
	// ErrMissingAuthToken indicates that bearer auth is missing its token.
	ErrMissingAuthToken = errors.New("missing auth token")
	// ErrMissingAuthValue indicates that token auth is missing its shared secret value.
	ErrMissingAuthValue = errors.New("missing auth value")
	// ErrMissingAuthLocation indicates that token auth does not define header or query lookup.
	ErrMissingAuthLocation = errors.New("missing auth location")
	// ErrUnexpectedAuthField indicates that an auth configuration set fields that are forbidden for its type.
	ErrUnexpectedAuthField = errors.New("unexpected auth field")
	// ErrInvalidActionType indicates that an action type is unknown.
	ErrInvalidActionType = errors.New("invalid action type")
	// ErrMissingActionTarget indicates that an action requiring a target is missing one.
	ErrMissingActionTarget = errors.New("missing action target")
	// ErrInvalidActionURL indicates that an outbound forwarding URL is invalid.
	ErrInvalidActionURL = errors.New("invalid action url")
)

// Config represents the persisted Fairway configuration loaded on daemon startup.
type Config struct {
	SchemaVersion string  `json:"schemaVersion"`
	Port          int     `json:"port"`
	Bind          string  `json:"bind"`
	MaxInFlight   int     `json:"maxInFlight,omitempty"`
	Routes        []Route `json:"routes"`
}

// Validate checks whether the config is internally consistent and supported by the daemon.
func (c Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: expected %q, got %q", ErrUnsupportedSchema, SchemaVersion, c.SchemaVersion)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("%w: %d", ErrInvalidPort, c.Port)
	}
	if net.ParseIP(c.Bind) == nil {
		return fmt.Errorf("%w: %q", ErrInvalidBind, c.Bind)
	}
	if c.MaxInFlight < 0 {
		return fmt.Errorf("%w: %d", ErrInvalidMaxInFlight, c.MaxInFlight)
	}

	seen := make(map[string]struct{}, len(c.Routes))
	for i, route := range c.Routes {
		if _, ok := seen[route.Path]; ok {
			return fmt.Errorf("%w: %q", ErrDuplicateRoutePath, route.Path)
		}
		seen[route.Path] = struct{}{}
		if err := route.Validate(); err != nil {
			return fmt.Errorf("route %d (%s): %w", i, route.Path, err)
		}
	}

	return nil
}

// Route represents a single HTTP path mapped to an auth policy and action.
type Route struct {
	Path    string        `json:"path"`
	Auth    Auth          `json:"auth"`
	Action  Action        `json:"action"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Validate checks whether the route path and nested auth/action settings are valid.
func (r Route) Validate() error {
	if !strings.HasPrefix(r.Path, "/") || strings.ContainsAny(r.Path, " *?#") {
		return fmt.Errorf("%w: %q", ErrInvalidRoutePath, r.Path)
	}
	if r.Timeout < 0 || r.Timeout > maxRouteTimeout {
		return fmt.Errorf("%w: %s", ErrInvalidRouteTimeout, r.Timeout)
	}
	if err := r.Auth.Validate(); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if err := r.Action.Validate(); err != nil {
		return fmt.Errorf("action: %w", err)
	}
	return nil
}

// AuthType identifies how an incoming request is authenticated for a route.
type AuthType string

const (
	// AuthBearer expects an Authorization Bearer token.
	AuthBearer AuthType = "bearer"
	// AuthToken expects a shared secret in a custom header or query parameter.
	AuthToken AuthType = "token"
	// AuthLocalOnly restricts the route to loopback requests without credentials.
	AuthLocalOnly AuthType = "local-only"
)

// Auth describes the authentication policy applied to a route.
type Auth struct {
	Type   AuthType `json:"type"`
	Token  string   `json:"token,omitempty"`
	Value  string   `json:"value,omitempty"`
	Header string   `json:"header,omitempty"`
	Query  string   `json:"query,omitempty"`
}

// Validate checks whether the auth configuration is compatible with its declared type.
func (a Auth) Validate() error {
	switch a.Type {
	case AuthBearer:
		if a.Token == "" {
			return ErrMissingAuthToken
		}
		if a.Value != "" || a.Header != "" || a.Query != "" {
			return fmt.Errorf("%w: bearer allows only token", ErrUnexpectedAuthField)
		}
	case AuthToken:
		if a.Value == "" {
			return ErrMissingAuthValue
		}
		if a.Header == "" && a.Query == "" {
			return ErrMissingAuthLocation
		}
		if a.Token != "" {
			return fmt.Errorf("%w: token auth does not use token field", ErrUnexpectedAuthField)
		}
	case AuthLocalOnly:
		if a.Token != "" || a.Value != "" || a.Header != "" || a.Query != "" {
			return fmt.Errorf("%w: local-only does not accept credentials", ErrUnexpectedAuthField)
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidAuthType, a.Type)
	}

	return nil
}

// ActionType identifies the Shipyard operation triggered by a matching route.
type ActionType string

const (
	// ActionCronRun executes `shipyard cron run <id>`.
	ActionCronRun ActionType = "cron.run"
	// ActionCronEnable executes `shipyard cron enable <id>`.
	ActionCronEnable ActionType = "cron.enable"
	// ActionCronDisable executes `shipyard cron disable <id>`.
	ActionCronDisable ActionType = "cron.disable"
	// ActionServiceStart executes `shipyard service start <id>`.
	ActionServiceStart ActionType = "service.start"
	// ActionServiceStop executes `shipyard service stop <id>`.
	ActionServiceStop ActionType = "service.stop"
	// ActionServiceRestart executes `shipyard service restart <id>`.
	ActionServiceRestart ActionType = "service.restart"
	// ActionMessageSend executes `shipyard message send ...`.
	ActionMessageSend ActionType = "message.send"
	// ActionTelegramHandle executes `shipyard message telegram handle`.
	ActionTelegramHandle ActionType = "telegram.handle"
	// ActionHTTPForward performs an outbound HTTP forward without invoking the core CLI.
	ActionHTTPForward ActionType = "http.forward"
)

// Action describes the operation executed after a route is authenticated.
type Action struct {
	Type     ActionType        `json:"type"`
	Target   string            `json:"target,omitempty"`
	Provider string            `json:"provider,omitempty"`
	URL      string            `json:"url,omitempty"`
	Method   string            `json:"method,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// Validate checks whether the action payload is sufficient for the selected action type.
func (a Action) Validate() error {
	switch a.Type {
	case ActionCronRun, ActionCronEnable, ActionCronDisable, ActionServiceStart, ActionServiceStop, ActionServiceRestart:
		if a.Target == "" {
			return ErrMissingActionTarget
		}
	case ActionMessageSend, ActionTelegramHandle:
		// No additional required fields in V1.
	case ActionHTTPForward:
		parsed, err := url.Parse(a.URL)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidActionURL, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("%w: unsupported scheme %q", ErrInvalidActionURL, parsed.Scheme)
		}
		if parsed.Host == "" {
			return fmt.Errorf("%w: missing host", ErrInvalidActionURL)
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidActionType, a.Type)
	}

	return nil
}
