package conversation

import (
	"context"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// Stateless is the no-op Store used when agent.Conversation.Mode is
// "stateless": Resolve returns the empty key, Lock is a no-op, Load returns
// an empty History and Save is a no-op.
type Stateless struct{}

func NewStateless() *Stateless { return &Stateless{} }

func (s *Stateless) Resolve(agent *crew.Agent, input map[string]any) (string, error) {
	return "", nil
}

// Lock is a no-op: without a persisted session there is nothing two
// concurrent runs could corrupt.
func (s *Stateless) Lock(ctx context.Context, agent *crew.Agent, key string) (func(), error) {
	return func() {}, nil
}

func (s *Stateless) Load(ctx context.Context, agent *crew.Agent, key string) (History, error) {
	return History{}, nil
}

func (s *Stateless) Save(ctx context.Context, agent *crew.Agent, key string, history History) error {
	return nil
}

var _ Store = (*Stateless)(nil)
