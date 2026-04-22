// Package crew contains the domain types of the shipyard crew addon.
// No serialization, IO or runtime logic lives here — just pure types and
// validation.
package crew

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const SchemaVersion = "1"

var (
	AgentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	ToolNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

type BackendType string

const (
	BackendCLI          BackendType = "cli"
	BackendAnthropicAPI BackendType = "anthropic_api"
)

type ExecutionMode string

const (
	ExecutionOnDemand ExecutionMode = "on-demand"
	ExecutionService  ExecutionMode = "service"
)

type ConversationMode string

const (
	ConversationStateless ConversationMode = "stateless"
	ConversationStateful  ConversationMode = "stateful"
)

type TriggerType string

const (
	TriggerCron    TriggerType = "cron"
	TriggerWebhook TriggerType = "webhook"
)

type ToolProtocol string

const (
	ToolExec ToolProtocol = "exec"
	ToolHTTP ToolProtocol = "http"
)

type Agent struct {
	Name         string       `yaml:"name"`
	Description  string       `yaml:"description"`
	Backend      Backend      `yaml:"backend"`
	Execution    Execution    `yaml:"execution"`
	Conversation Conversation `yaml:"conversation"`
	Triggers     []Trigger    `yaml:"triggers"`
	Tools        []Tool       `yaml:"tools"`
	PromptPath   string       `yaml:"-"`
	Dir          string       `yaml:"-"`
}

type Backend struct {
	Type    BackendType `yaml:"type"`
	Command []string    `yaml:"command,omitempty"`
	Model   string      `yaml:"model,omitempty"`
	// SystemPromptFlag overrides the argv flag used to inject prompt.md into
	// the subprocess when Type is "cli". When empty, the backend uses the
	// Claude Code default ("--append-system-prompt"). The prompt value is
	// always passed as the flag's argument — never omitted.
	SystemPromptFlag string `yaml:"system_prompt_flag,omitempty"`
}

type Execution struct {
	Mode ExecutionMode `yaml:"mode"`
	Pool string        `yaml:"pool"`
}

type Conversation struct {
	Mode ConversationMode `yaml:"mode"`
	Key  string           `yaml:"key,omitempty"`
}

type Trigger struct {
	Type     TriggerType `yaml:"type"`
	Schedule string      `yaml:"schedule,omitempty"`
	Route    string      `yaml:"route,omitempty"`
}

type Tool struct {
	Name        string            `yaml:"name"`
	Protocol    ToolProtocol      `yaml:"protocol"`
	Description string            `yaml:"description,omitempty"`
	InputSchema map[string]string `yaml:"input_schema,omitempty"`
	Command     []string          `yaml:"command,omitempty"`
	Method      string            `yaml:"method,omitempty"`
	URL         string            `yaml:"url,omitempty"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	Body        string            `yaml:"body,omitempty"`
}

func (a Agent) Validate() error {
	if !AgentNameRe.MatchString(a.Name) {
		return fmt.Errorf("agent.name: must match %s", AgentNameRe)
	}
	if err := a.Backend.Validate(); err != nil {
		return fmt.Errorf("agent.backend: %w", err)
	}
	if err := a.Execution.Validate(); err != nil {
		return fmt.Errorf("agent.execution: %w", err)
	}
	if err := a.Conversation.Validate(); err != nil {
		return fmt.Errorf("agent.conversation: %w", err)
	}
	for i, tr := range a.Triggers {
		if err := tr.Validate(); err != nil {
			return fmt.Errorf("agent.triggers[%d]: %w", i, err)
		}
	}
	for i, tl := range a.Tools {
		if err := tl.Validate(); err != nil {
			return fmt.Errorf("agent.tools[%d]: %w", i, err)
		}
	}
	names := map[string]struct{}{}
	for i, tl := range a.Tools {
		if _, dup := names[tl.Name]; dup {
			return fmt.Errorf("agent.tools: duplicate tool name %q at index %d", tl.Name, i)
		}
		names[tl.Name] = struct{}{}
	}
	seenTrig := map[string]struct{}{}
	for i, tr := range a.Triggers {
		key := string(tr.Type) + "|" + tr.Schedule + "|" + tr.Route
		if _, dup := seenTrig[key]; dup {
			return fmt.Errorf("agent.triggers: duplicate trigger at index %d", i)
		}
		seenTrig[key] = struct{}{}
	}
	return nil
}

func (b Backend) Validate() error {
	switch b.Type {
	case BackendCLI:
		if len(b.Command) == 0 {
			return errors.New(`type "cli" requires non-empty command`)
		}
		if b.Model != "" {
			return errors.New(`type "cli" must not set model`)
		}
		if b.SystemPromptFlag != "" && strings.TrimSpace(b.SystemPromptFlag) != b.SystemPromptFlag {
			return errors.New(`system_prompt_flag must not have surrounding whitespace`)
		}
	case BackendAnthropicAPI:
		if strings.TrimSpace(b.Model) == "" {
			return errors.New(`type "anthropic_api" requires model`)
		}
		if len(b.Command) > 0 {
			return errors.New(`type "anthropic_api" must not set command`)
		}
		if b.SystemPromptFlag != "" {
			return errors.New(`type "anthropic_api" must not set system_prompt_flag`)
		}
	default:
		return fmt.Errorf(`invalid type %q: must be "cli" or "anthropic_api"`, b.Type)
	}
	return nil
}

func (e Execution) Validate() error {
	switch e.Mode {
	case ExecutionOnDemand, ExecutionService:
	default:
		return fmt.Errorf(`invalid mode %q: must be "on-demand" or "service"`, e.Mode)
	}
	if strings.TrimSpace(e.Pool) == "" {
		return errors.New("pool must be non-empty")
	}
	return nil
}

func (c Conversation) Validate() error {
	switch c.Mode {
	case ConversationStateless:
		if c.Key != "" {
			return errors.New(`mode "stateless" must not set key`)
		}
	case ConversationStateful:
		if strings.TrimSpace(c.Key) == "" {
			return errors.New(`mode "stateful" requires key`)
		}
	default:
		return fmt.Errorf(`invalid mode %q: must be "stateless" or "stateful"`, c.Mode)
	}
	return nil
}

func (t Trigger) Validate() error {
	switch t.Type {
	case TriggerCron:
		if strings.TrimSpace(t.Schedule) == "" {
			return errors.New(`type "cron" requires schedule`)
		}
		if t.Route != "" {
			return errors.New(`type "cron" must not set route`)
		}
	case TriggerWebhook:
		if !strings.HasPrefix(t.Route, "/") {
			return errors.New(`type "webhook" requires route starting with "/"`)
		}
		if t.Schedule != "" {
			return errors.New(`type "webhook" must not set schedule`)
		}
	default:
		return fmt.Errorf(`invalid type %q: must be "cron" or "webhook"`, t.Type)
	}
	return nil
}

func (t Tool) Validate() error {
	if !ToolNameRe.MatchString(t.Name) {
		return fmt.Errorf("name %q: must match %s", t.Name, ToolNameRe)
	}
	switch t.Protocol {
	case ToolExec:
		if len(t.Command) == 0 {
			return errors.New(`protocol "exec" requires non-empty command`)
		}
		if t.Method != "" || t.URL != "" || len(t.Headers) > 0 || t.Body != "" {
			return errors.New(`protocol "exec" must not set http fields`)
		}
	case ToolHTTP:
		if !isValidHTTPMethod(t.Method) {
			return fmt.Errorf(`protocol "http" requires method in {GET,POST,PUT,PATCH,DELETE}, got %q`, t.Method)
		}
		if strings.TrimSpace(t.URL) == "" {
			return errors.New(`protocol "http" requires url`)
		}
		if _, err := url.Parse(t.URL); err != nil {
			return fmt.Errorf("invalid url %q: %w", t.URL, err)
		}
		if len(t.Command) > 0 {
			return errors.New(`protocol "http" must not set command`)
		}
	default:
		return fmt.Errorf(`invalid protocol %q: must be "exec" or "http"`, t.Protocol)
	}
	for field, typ := range t.InputSchema {
		if !isValidSchemaType(typ) {
			return fmt.Errorf("input_schema[%q]: invalid type %q (allowed: string,number,boolean,object,array)", field, typ)
		}
	}
	return nil
}

func isValidHTTPMethod(m string) bool {
	switch m {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

func isValidSchemaType(t string) bool {
	switch t {
	case "string", "number", "boolean", "object", "array":
		return true
	}
	return false
}
