package fairwayctl

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var (
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
