package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/events"
	"github.com/fernandotonon/QtMesh-Agents/internal/scheduler"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
	"github.com/fernandotonon/QtMesh-Agents/internal/worktree"
)

// Executor runs a single orchestrated task.
type Executor struct {
	Registry        *agents.Registry
	Store           *storage.Store
	Log             *slog.Logger
	UseWorktrees    bool
	WorktreeBase    string
	Timeout         time.Duration
	MaxRetries      int
	RequireApproval bool
	Emit            func(ctx context.Context, kind events.Kind, runID, taskID, agentID, msg string) error
}

func (e *Executor) RunTask(ctx context.Context, runID string, req PlanRequest, task *scheduler.Task) error {
	maxRetries := task.MaxRetries
	if maxRetries <= 0 {
		maxRetries = e.MaxRetries
	}
	if maxRetries <= 0 {
		maxRetries = 2
	}

	task.Status = scheduler.StatusRunning
	_ = e.persist(ctx, runID, task)

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			task.Status = scheduler.StatusCancelled
			_ = e.persist(ctx, runID, task)
			return ctx.Err()
		}
		task.RetryCount = attempt
		lastErr = e.runOnce(ctx, runID, req, task)
		if lastErr == nil {
			task.Status = scheduler.StatusSucceeded
			task.Error = ""
			_ = e.persist(ctx, runID, task)
			return nil
		}
		task.Error = lastErr.Error()
		_ = e.persist(ctx, runID, task)
		if attempt < maxRetries {
			e.log().Warn("task retry", "task", task.ID, "attempt", attempt+1, "err", lastErr)
		}
	}
	task.Status = scheduler.StatusFailed
	_ = e.persist(ctx, runID, task)
	return lastErr
}

func shouldIsolate(task *scheduler.Task) bool {
	switch task.Category {
	case "architecture", "review":
		return false
	}
	id := strings.ToLower(task.ID)
	if strings.HasPrefix(id, "discover") || strings.HasPrefix(id, "integrate") || strings.HasPrefix(id, "review") {
		return false
	}
	return true
}

func (e *Executor) runOnce(ctx context.Context, runID string, req PlanRequest, task *scheduler.Task) error {
	e.log().Info("task start", "run", runID, "task", task.ID, "agent", task.AssignedAgent)
	if e.Emit != nil {
		_ = e.Emit(ctx, events.KindTaskStarted, runID, task.ID, task.AssignedAgent, task.Description)
	}

	workDir := req.WorkDir
	useWT := e.UseWorktrees && isGitRepo(req.WorkDir) && shouldIsolate(task)
	if useWT {
		mgr := worktree.New(req.WorkDir, e.WorktreeBase)
		branch := "qma/" + runID + "/" + task.ID
		dir, err := mgr.Create(ctx, runID+"-"+task.ID, branch)
		if err != nil {
			// If branch/worktree exists from a retry, try a unique suffix.
			branch = fmt.Sprintf("qma/%s/%s-%d", runID, task.ID, time.Now().UnixNano())
			dir, err = mgr.Create(ctx, fmt.Sprintf("%s-%s-%d", runID, task.ID, time.Now().UnixNano()), branch)
			if err != nil {
				return fmt.Errorf("worktree: %w", err)
			}
		}
		task.WorktreePath = dir
		task.Branch = branch
		workDir = dir
	}

	if e.Registry == nil {
		return fmt.Errorf("agent registry not configured")
	}
	if task.AssignedAgent == "" {
		return fmt.Errorf("task %q has no assigned agent", task.ID)
	}
	adapter, err := e.Registry.Require(task.AssignedAgent)
	if err != nil {
		return err
	}

	prompt := buildTaskPrompt(req, task, workDir)
	tctx := ctx
	cancel := func() {}
	if e.Timeout > 0 {
		tctx, cancel = context.WithTimeout(ctx, e.Timeout)
	}
	defer cancel()

	execRes, err := adapter.Execute(tctx, agents.AgentTask{
		ID:          runID + "-" + task.ID,
		Description: task.Description,
		WorkDir:     workDir,
		Prompt:      prompt,
		Timeout:     e.Timeout,
	})

	result := map[string]any{
		"exit_code": execRes.ExitCode,
		"output":    trimOut(execRes.Output),
		"agent":     task.AssignedAgent,
		"work_dir":  workDir,
	}
	if err != nil {
		result["error"] = err.Error()
	}
	raw, _ := json.Marshal(result)
	task.ResultJSON = string(raw)

	// Capture diff when using a worktree.
	if task.WorktreePath != "" {
		diff, derr := worktree.New(req.WorkDir, e.WorktreeBase).Diff(ctx, task.WorktreePath)
		if derr == nil {
			dataDir, _ := config.DataDir()
			dir := DiffDir(dataDir, runID)
			_ = EnsureDir(dir)
			path := filepath.Join(dir, task.ID+".diff")
			if werr := os.WriteFile(path, []byte(diff), 0o600); werr == nil {
				task.DiffPath = path
			}
		}
	}

	if err != nil {
		return err
	}
	if execRes.ExitCode != 0 {
		return fmt.Errorf("agent exit %d: %s", execRes.ExitCode, trimOut(execRes.Output))
	}

	// Integrate task: never merge; just summarize.
	if task.ID == "integrate" || task.Category == "review" {
		_ = e.RequireApproval // documented behavior
	}

	if e.Emit != nil {
		_ = e.Emit(ctx, events.KindTaskCompleted, runID, task.ID, task.AssignedAgent, "task succeeded")
	}
	return nil
}

func (e *Executor) persist(ctx context.Context, runID string, task *scheduler.Task) error {
	rec := taskToRecord(runID, *task)
	return e.Store.UpsertTask(ctx, rec)
}

func (e *Executor) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func buildTaskPrompt(req PlanRequest, task *scheduler.Task, workDir string) string {
	var b strings.Builder
	b.WriteString("You are a worker agent in QtMesh Agents orchestration.\n")
	b.WriteString("Overall objective: " + req.Objective + "\n")
	b.WriteString("Your task id: " + task.ID + "\n")
	b.WriteString("Your scoped objective: " + task.Description + "\n")
	b.WriteString("Working directory: " + workDir + "\n")
	b.WriteString("Acceptance criteria:\n")
	for _, a := range task.Acceptance {
		b.WriteString(" - " + a + "\n")
	}
	b.WriteString("\nRules:\n")
	b.WriteString("- Stay narrowly scoped to this task.\n")
	b.WriteString("- Do not merge to main/master or force-push.\n")
	b.WriteString("- Do not discard unrelated user changes.\n")
	b.WriteString("- Prefer minimal, reviewable diffs.\n")
	if task.ID == "integrate" || task.Category == "review" {
		b.WriteString("- Summarize changes across tasks; do NOT merge protected branches.\n")
		b.WriteString("- State clearly that human approval is required before merge.\n")
	}
	if task.ID == "discover" || task.Category == "architecture" {
		b.WriteString("- Analyze and report; avoid large refactors in this task.\n")
	}
	return b.String()
}

func isGitRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}
