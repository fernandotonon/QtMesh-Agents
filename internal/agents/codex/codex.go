package codex

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

// Adapter wraps the OpenAI Codex CLI.
type Adapter struct {
	agents.BaseAdapter
	Sandbox      string // read-only | workspace-write | danger-full-access
	ApproveForMe bool   // headless: force non-interactive approvals
}

// New creates a Codex adapter with sensible automation defaults.
func New(command string, args []string) *Adapter {
	if command == "" {
		command = "codex"
	}
	return &Adapter{
		BaseAdapter:  agents.NewBaseAdapter("codex", "OpenAI", command, args),
		Sandbox:      "workspace-write",
		ApproveForMe: true,
	}
}

// NewWithOptions creates a Codex adapter with sandbox/approval policy.
func NewWithOptions(command string, args []string, sandbox string, approveForMe bool) *Adapter {
	a := New(command, args)
	if sandbox != "" {
		a.Sandbox = sandbox
	}
	a.ApproveForMe = approveForMe
	return a
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
// Prompt is passed on stdin ("-") so large orchestration prompts never hit ARG_MAX
// and Codex does not block on "Reading additional input from stdin...".
//
// For ApproveForMe (default), we force approval_policy=never + workspace-write.
// --approve-for-me alone leaves approval: on-request, which hangs when the user's
// Codex config has approvals_reviewer="user" (waits for ChatGPT app confirmation).
func (a *Adapter) Execute(ctx context.Context, task agents.AgentTask) (agents.Execution, error) {
	sandbox := a.Sandbox
	if sandbox == "" {
		sandbox = "workspace-write"
	}
	if task.ElevatedSandbox {
		sandbox = "danger-full-access"
	}
	args := []string{
		"exec",
		"--skip-git-repo-check",
	}
	switch {
	case task.InteractiveApprovals:
		// User opted in: Codex will prompt in the ChatGPT/Codex app (on-request).
		args = append(args,
			"--approve-for-me",
			"-c", `approvals_reviewer="user"`,
		)
	case a.ApproveForMe:
		// True unattended mode: never pause for human approval.
		// Do not use --approve-for-me here — it keeps on-request and can wait forever.
		args = append(args,
			"-c", `approval_policy="never"`,
			"-c", `approvals_reviewer="auto_review"`,
			"--sandbox", sandbox,
		)
	default:
		args = append(args, "--sandbox", sandbox)
	}
	// Interactive mode cannot combine --approve-for-me with --sandbox; elevated
	// access in interactive mode uses danger-full-access via config override.
	if task.InteractiveApprovals && task.ElevatedSandbox {
		args = append(args, "-c", `sandbox_mode="danger-full-access"`)
	}
	if task.WorkDir != "" {
		args = append(args, "-C", task.WorkDir)
	}
	args = append(args, task.ExtraArgs...)
	args = append(args, "-") // read prompt from stdin

	started := time.Now()
	cmd := exec.CommandContext(ctx, a.Command(), args...)
	if task.WorkDir != "" {
		cmd.Dir = task.WorkDir
	}
	cmd.Env = os.Environ()
	for k, v := range task.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(task.Prompt)
	agents.SetHeadlessProcGroup(cmd)

	// Reuse shared headless runner path via a thin local watcher by calling
	// through Execute-equivalent Start/Wait with the same streaming writer.
	watcher := agents.NewOutputWatcher(task.OnOutput)
	cmd.Stdout = watcher
	cmd.Stderr = watcher

	err := cmd.Start()
	if err == nil {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
		case <-ctx.Done():
			agents.KillHeadlessProcGroup(cmd)
			err = <-done
			if err == nil {
				err = ctx.Err()
			}
		}
	}
	ended := time.Now()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	id := task.ID
	if id == "" {
		id = "codex-exec"
	}
	return agents.Execution{
		ID: id, AgentID: a.ID(), ExitCode: exitCode, Output: watcher.String(),
		StartedAt: started, EndedAt: ended, Err: err,
	}, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
