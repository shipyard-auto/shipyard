// Package conversation defines the persistence contract for agent
// conversation history and provides the stateless/stateful implementations
// consumed by backends and the runner.
package conversation

import (
	"context"
	"encoding/json"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// History carries the conversation context of an agent. Only one of the
// fields below is significant at a time:
//   - backend api: Messages contains the full history replayed every call.
//   - backend cli: SessionID is the opaque key of the external CLI (ex:
//     `claude --resume <id>`).
type History struct {
	Messages  []Message `json:"messages,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
}

// Message is a single backend-native message. Content is kept as raw JSON to
// accommodate the heterogeneous blocks produced by providers (text,
// tool_use, tool_result, ...) without re-modelling the schema here.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Store is the persistence contract for agent conversation history.
//
// Resolve: given the agent and the trigger input, returns the conversation
//
//	key that uniquely identifies a session. Always "" in stateless mode.
//
// Load:    given a key, returns the stored history (or the zero value).
// Save:    persists the updated history for the given key.
type Store interface {
	Resolve(agent *crew.Agent, input map[string]any) (string, error)
	Load(ctx context.Context, agent *crew.Agent, key string) (History, error)
	Save(ctx context.Context, agent *crew.Agent, key string, history History) error
}
