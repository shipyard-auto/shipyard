// Package backend defines the common contract (Backend, ToolDispatcher,
// RunInput, RunOutput, Usage) shared by every concrete backend
// implementation (anthropic_api, cli, ...) used by the crew runner.
//
// Each backend is responsible for producing the final text response for an
// agent turn given a system prompt, a user message and the previous history.
// Backends that support tool use dispatch tool calls through the provided
// ToolDispatcher; backends that delegate tool orchestration to an external
// process (e.g. a CLI) simply ignore the dispatcher.
package backend

import (
	"context"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/conversation"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/tools"
)

// RunInput carries everything a backend needs to produce one turn.
//
//   - Prompt is the rendered system prompt (contents of prompt.md).
//   - User is the user-side message derived from the trigger input.
//   - History is the previously persisted conversation state
//     (Messages for API-style backends, SessionID for CLI-style backends).
//   - Agent is the full validated agent definition; backends read
//     Agent.Backend.{Model,Command} and Agent.Tools from it.
type RunInput struct {
	Prompt  string
	User    string
	History conversation.History
	Agent   *crew.Agent
}

// RunOutput carries the text to surface to the trigger and the updated
// history to persist after the turn. Usage reports tokens when the backend
// knows them; CLI-style backends may leave it zero.
type RunOutput struct {
	Text    string
	History conversation.History
	Usage   Usage
}

// Usage reports the token counts consumed by a single Run. Fields are
// cumulative across any tool-use loop performed inside Run.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ToolDispatcher is the subset of the tools.Dispatcher contract that the
// backend depends on. It is intentionally agent-less: the runner binds the
// agent to the dispatcher before handing it over, so the backend does not
// need to know which agent is active.
type ToolDispatcher interface {
	Call(ctx context.Context, toolName string, input map[string]any) (tools.Envelope, error)
}

// Backend is the single contract every concrete backend must satisfy.
// Implementations must honour ctx cancellation across any I/O or subprocess
// invocation they perform.
type Backend interface {
	Run(ctx context.Context, in RunInput, dispatch ToolDispatcher) (RunOutput, error)
}
