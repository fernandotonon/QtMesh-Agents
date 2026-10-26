package claude

import (
	"context"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

// Adapter wraps the Claude Code CLI.
type Adapter struct {
	agents.BaseAdapter
}

// New creates a Claude adapter.
func New(command string, args []string) *Adapter {
	if command == "" {
		command = "claude"
	}
	return &Adapter{BaseAdapter: agents.NewBaseAdapter("claude", "Anthropic", command, args)}
}

// Capabilities probes Claude CLI features.
func (a *Adapter) Capabilities(ctx context.Context) (agents.AgentCapabilities, error) {
	caps := agents.AgentCapabilities{
		Interactive:      true,
		Headless:         true,
		SessionRestore:   true,
		StructuredOutput: true,
		UsageReporting:   false,
	}
	if ver, err := a.VersionProbe(ctx, "--version"); err == nil {
		caps.Version = firstLine(ver)
	}
	return caps, nil
}

// StartSession launches an interactive Claude Code session.
func (a *Adapter) StartSession(ctx context.Context, opts agents.SessionOptions) (agents.AgentSession, error) {
	extra := append([]string{}, opts.ExtraArgs...)
	if opts.ResumeID != "" {
		extra = append(extra, "--resume", opts.ResumeID)
	}
	return a.StartPTY(ctx, opts, extra...)
}

// Execute runs a headless Claude prompt.
// Uses `claude -p` for print/headless mode when available.
func (a *Adapter) Execute(ctx context.Context, task agents.AgentTask) (agents.Execution, error) {
	args := []string{"-p", task.Prompt, "--output-format", "text"}
	args = append(args, task.ExtraArgs...)
	return a.RunHeadless(ctx, task, args)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
