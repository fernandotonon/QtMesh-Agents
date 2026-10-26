package codex

import (
	"context"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

// Adapter wraps the OpenAI Codex CLI.
type Adapter struct {
	agents.BaseAdapter
}

// New creates a Codex adapter.
func New(command string, args []string) *Adapter {
	if command == "" {
		command = "codex"
	}
	return &Adapter{BaseAdapter: agents.NewBaseAdapter("codex", "OpenAI", command, args)}
}

// Capabilities probes Codex CLI features.
func (a *Adapter) Capabilities(ctx context.Context) (agents.AgentCapabilities, error) {
	caps := agents.AgentCapabilities{
		Interactive:      true,
		Headless:         true,
		SessionRestore:   false,
		StructuredOutput: true,
		UsageReporting:   false,
	}
	if ver, err := a.VersionProbe(ctx, "--version"); err == nil {
		caps.Version = firstLine(ver)
	}
	return caps, nil
}

// StartSession launches an interactive Codex session.
func (a *Adapter) StartSession(ctx context.Context, opts agents.SessionOptions) (agents.AgentSession, error) {
	extra := append([]string{}, opts.ExtraArgs...)
	return a.StartPTY(ctx, opts, extra...)
}

// Execute runs a headless Codex prompt via `codex exec`.
func (a *Adapter) Execute(ctx context.Context, task agents.AgentTask) (agents.Execution, error) {
	args := []string{"exec", task.Prompt}
	args = append(args, task.ExtraArgs...)
	return a.RunHeadless(ctx, task, args)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
