package cursor

import (
	"context"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

// Adapter wraps the Cursor Agent CLI.
type Adapter struct {
	agents.BaseAdapter
}

// New creates a Cursor adapter.
func New(command string, args []string) *Adapter {
	if command == "" {
		command = "cursor-agent"
	}
	return &Adapter{BaseAdapter: agents.NewBaseAdapter("cursor", "Cursor", command, args)}
}

// Capabilities probes Cursor Agent CLI features.
func (a *Adapter) Capabilities(ctx context.Context) (agents.AgentCapabilities, error) {
	caps := agents.AgentCapabilities{
		Interactive:      true,
		Headless:         true,
		SessionRestore:   false,
		StructuredOutput: false,
		UsageReporting:   false,
	}
	if ver, err := a.VersionProbe(ctx, "--version"); err == nil {
		caps.Version = firstLine(ver)
	} else if ver, err := a.VersionProbe(ctx, "version"); err == nil {
		caps.Version = firstLine(ver)
	}
	return caps, nil
}

// StartSession launches an interactive Cursor Agent session.
func (a *Adapter) StartSession(ctx context.Context, opts agents.SessionOptions) (agents.AgentSession, error) {
	extra := append([]string{}, opts.ExtraArgs...)
	return a.StartPTY(ctx, opts, extra...)
}

// Execute runs a headless Cursor Agent prompt.
func (a *Adapter) Execute(ctx context.Context, task agents.AgentTask) (agents.Execution, error) {
	// Worktrees are new workspaces; headless execution cannot answer trust prompts.
	args := append(a.Args(), "--trust", "-p", task.Prompt)
	args = append(args, task.ExtraArgs...)
	return a.RunHeadless(ctx, task, args)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
