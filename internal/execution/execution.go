package execution

import (
	"context"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

// Runner executes headless agent tasks with timeouts.
type Runner struct {
	Registry *agents.Registry
	Timeout  time.Duration
}

func (r *Runner) Run(ctx context.Context, agentID string, task agents.AgentTask) (agents.Execution, error) {
	adapter, err := r.Registry.Require(agentID)
	if err != nil {
		return agents.Execution{}, err
	}
	timeout := r.Timeout
	if task.Timeout > 0 {
		timeout = task.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return adapter.Execute(ctx, task)
}
