package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/conversation"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

const (
	anthropicEndpoint = "https://api.anthropic.com/v1/messages"
	anthropicVersion  = "2023-06-01"

	// MaxIterations caps the number of request/response cycles the tool-use
	// loop may run inside a single Run call. Exceeding it is always an
	// error — production agents should converge well below this bound.
	MaxIterations = 20

	defaultMaxTokens = 4096
	maxResponseBytes = 16 * 1024 * 1024
)

// APIBackend implements Backend against the Anthropic Messages API via raw
// net/http. It owns no global state; construct one per process or per
// runner.
type APIBackend struct {
	httpClient *http.Client
	endpoint   string
}

// APIOption customises an APIBackend at construction time. Use
// WithEndpoint and WithHTTPClient in tests.
type APIOption func(*APIBackend)

// WithEndpoint overrides the Messages API endpoint, typically to point a
// test at an httptest.Server.
func WithEndpoint(url string) APIOption {
	return func(b *APIBackend) { b.endpoint = url }
}

// WithHTTPClient overrides the default http.Client, typically to inject a
// shorter timeout or a custom transport.
func WithHTTPClient(c *http.Client) APIOption {
	return func(b *APIBackend) {
		if c != nil {
			b.httpClient = c
		}
	}
}

// NewAPIBackend returns an APIBackend pointed at the production endpoint
// with a default http.Client. Apply options to override either field.
func NewAPIBackend(opts ...APIOption) *APIBackend {
	b := &APIBackend{
		httpClient: &http.Client{},
		endpoint:   anthropicEndpoint,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

var _ Backend = (*APIBackend)(nil)

type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Messages  []apiMessage `json:"messages"`
	Tools     []apiToolDef `json:"tools,omitempty"`
}

type apiMessage struct {
	Role    string     `json:"role"`
	Content []apiBlock `json:"content"`
}

type apiBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   any             `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type apiToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type apiUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type apiResponse struct {
	Content    []apiBlock `json:"content"`
	StopReason string     `json:"stop_reason"`
	Usage      apiUsage   `json:"usage"`
	Error      *apiError  `json:"error,omitempty"`
}

// Run executes the tool-use loop against the Anthropic Messages API. The
// loop reads Agent.Backend.Model, Agent.Tools and the previous history,
// appending the new user turn, and keeps re-calling the API until the
// server signals a terminal stop_reason or MaxIterations is reached.
func (b *APIBackend) Run(ctx context.Context, in RunInput, disp ToolDispatcher) (RunOutput, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return RunOutput{}, errors.New("ANTHROPIC_API_KEY not set")
	}
	if in.Agent == nil {
		return RunOutput{}, errors.New("api backend: nil agent")
	}

	msgs := historyToAPI(in.History)
	msgs = append(msgs, apiMessage{
		Role:    "user",
		Content: []apiBlock{{Type: "text", Text: in.User}},
	})

	var finalText strings.Builder
	var totalUsage Usage

	for i := 0; i < MaxIterations; i++ {
		if err := ctx.Err(); err != nil {
			return RunOutput{}, err
		}

		req := apiRequest{
			Model:     in.Agent.Backend.Model,
			MaxTokens: defaultMaxTokens,
			System:    in.Prompt,
			Messages:  msgs,
			Tools:     toolsToAPI(in.Agent.Tools),
		}
		resp, err := b.call(ctx, apiKey, req)
		if err != nil {
			return RunOutput{}, err
		}

		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		msgs = append(msgs, apiMessage{Role: "assistant", Content: resp.Content})

		if resp.StopReason != "tool_use" {
			for _, bl := range resp.Content {
				if bl.Type != "text" {
					continue
				}
				if finalText.Len() > 0 {
					finalText.WriteString("\n")
				}
				finalText.WriteString(bl.Text)
			}
			return RunOutput{
				Text:    finalText.String(),
				History: apiToHistory(msgs),
				Usage:   totalUsage,
			}, nil
		}

		toolResults := b.dispatchToolUses(ctx, resp.Content, disp)
		msgs = append(msgs, apiMessage{Role: "user", Content: toolResults})
	}

	return RunOutput{}, fmt.Errorf("exceeded MaxIterations (%d)", MaxIterations)
}

func (b *APIBackend) dispatchToolUses(ctx context.Context, blocks []apiBlock, disp ToolDispatcher) []apiBlock {
	var out []apiBlock
	for _, bl := range blocks {
		if bl.Type != "tool_use" {
			continue
		}
		var input map[string]any
		if len(bl.Input) > 0 {
			if err := json.Unmarshal(bl.Input, &input); err != nil {
				out = append(out, apiBlock{
					Type:      "tool_result",
					ToolUseID: bl.ID,
					Content:   fmt.Sprintf("invalid tool input: %v", err),
					IsError:   true,
				})
				continue
			}
		}
		if disp == nil {
			out = append(out, apiBlock{
				Type:      "tool_result",
				ToolUseID: bl.ID,
				Content:   "no tool dispatcher configured",
				IsError:   true,
			})
			continue
		}
		env, err := disp.Call(ctx, bl.Name, input)
		if err != nil {
			out = append(out, apiBlock{
				Type:      "tool_result",
				ToolUseID: bl.ID,
				Content:   err.Error(),
				IsError:   true,
			})
			continue
		}
		envJSON, merr := json.Marshal(env)
		if merr != nil {
			out = append(out, apiBlock{
				Type:      "tool_result",
				ToolUseID: bl.ID,
				Content:   fmt.Sprintf("envelope marshal: %v", merr),
				IsError:   true,
			})
			continue
		}
		out = append(out, apiBlock{
			Type:      "tool_result",
			ToolUseID: bl.ID,
			Content:   string(envJSON),
			IsError:   !env.Ok,
		})
	}
	return out
}

func (b *APIBackend) call(ctx context.Context, apiKey string, body apiRequest) (*apiResponse, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic call: %w", err)
	}
	defer resp.Body.Close()

	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("anthropic api %d: %s", resp.StatusCode, string(buf))
	}

	var out apiResponse
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("anthropic error: %s", out.Error.Message)
	}
	return &out, nil
}

func toolsToAPI(ts []crew.Tool) []apiToolDef {
	if len(ts) == 0 {
		return nil
	}
	out := make([]apiToolDef, 0, len(ts))
	for _, t := range ts {
		schema := tools.BuildJSONSchema(t.InputSchema)
		raw, _ := json.Marshal(schema)
		out = append(out, apiToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: raw,
		})
	}
	return out
}

func historyToAPI(h conversation.History) []apiMessage {
	if len(h.Messages) == 0 {
		return nil
	}
	out := make([]apiMessage, 0, len(h.Messages))
	for _, m := range h.Messages {
		var content []apiBlock
		if len(m.Content) > 0 {
			_ = json.Unmarshal(m.Content, &content)
		}
		out = append(out, apiMessage{Role: m.Role, Content: content})
	}
	return out
}

func apiToHistory(msgs []apiMessage) conversation.History {
	if len(msgs) == 0 {
		return conversation.History{}
	}
	out := make([]conversation.Message, 0, len(msgs))
	for _, m := range msgs {
		raw, err := json.Marshal(m.Content)
		if err != nil {
			raw = []byte("[]")
		}
		out = append(out, conversation.Message{Role: m.Role, Content: raw})
	}
	return conversation.History{Messages: out}
}
