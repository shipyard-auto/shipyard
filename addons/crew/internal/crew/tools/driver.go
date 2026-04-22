package tools

import (
	"context"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew"
)

// DriverContext carries per-invocation metadata that the dispatcher injects
// into each tool call. AgentName/AgentDir populate the {{agent.*}} namespace
// of the template engine; Env populates {{env.*}} and, for exec tools, is
// also merged into the child process environment.
type DriverContext struct {
	AgentName string
	AgentDir  string
	Env       map[string]string
}

// Driver is the common contract implemented by every protocol-specific tool
// backend (exec, http, ...). Implementations translate normal failure modes
// (timeout, crash, bad payload) into Failure envelopes and reserve the Go
// error return for programming violations like wrong protocol or empty
// command — conditions that indicate a contract breach rather than a tool
// runtime problem.
type Driver interface {
	Execute(ctx context.Context, tool crew.Tool, input map[string]any, dc DriverContext) (Envelope, error)
}
