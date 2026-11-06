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
	"sync"
	"sync/atomic"
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
	RaiseAttention  func(ctx context.Context, a storage.Attention) error
	// OnQuotaExhausted is called when a vendor reports token/credit exhaustion
	// so the engine can mark the agent and reseat leadership.
	OnQuotaExhausted func(agentID, reason string)
	IsExhausted      func(agentID string) bool
	ProjectID        string
	BenchID          string
	Emit             func(ctx context.Context, kind events.Kind, runID, taskID, agentID, msg string) error

	fallbackAgents []string
	attnFP         map[string]int
	muFB           sync.Mutex
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

	// Extra budget when hopping vendors on quota so we don't burn retries on one exhausted agent.
	effectiveMax := maxRetries
	quotaHops := 0

	var lastErr error
	for attempt := 0; attempt <= effectiveMax; attempt++ {
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
		// Hard Needs you — do not retry.
		if task.Status == scheduler.StatusAwaitingInput ||
			(lastErr != nil && strings.Contains(lastErr.Error(), "needs attention")) {
			_ = e.persist(ctx, runID, task)
			return lastErr
		}
		// Quota/token exhaustion or empty exit -1: hop to the next fallback agent.
		if attempt < effectiveMax && (isEmptyExitRetryable(lastErr) || isQuotaRetryable(lastErr) || isSoftStallRetryable(lastErr)) {
			prev := task.AssignedAgent
			e.tryFallbackAgent(task)
			if isQuotaRetryable(lastErr) && task.AssignedAgent != prev && quotaHops < 3 {
				quotaHops++
				effectiveMax++ // don't spend the budget on the exhausted vendor
			}
		}
		task.Error = lastErr.Error()
		_ = e.persist(ctx, runID, task)
		if attempt < effectiveMax {
			e.log().Warn("task retry", "task", task.ID, "attempt", attempt+1, "err", lastErr)
		}
	}
	task.Status = scheduler.StatusFailed
	_ = e.persist(ctx, runID, task)
	return lastErr
}

func shouldIsolate(task *scheduler.Task) bool {
	switch task.Category {
	case "architecture", "review", "leadership":
		return false
	}
	id := strings.ToLower(task.ID)
	if strings.Contains(id, "discover") || strings.Contains(id, "integrate") ||
		strings.Contains(id, "review") || strings.HasSuffix(id, "lead") {
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
	if useWT && task.WorktreePath != "" {
		if !isGitRepo(task.WorktreePath) {
			return fmt.Errorf("saved task worktree unavailable: %s", task.WorktreePath)
		}
		workDir = task.WorktreePath
	} else if useWT {
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
		if err := e.prepareDependencies(ctx, runID, task, dir); err != nil {
			return err
		}
		task.WorktreePath = dir
		task.Branch = branch
		workDir = dir
	}

	// Leadership is local and instant — avoids hanging the whole bench on a slow LLM.
	if isLeadershipTask(task) {
		if err := runLocalLead(ctx, req, task); err != nil {
			return err
		}
		task.AssignedAgent = "local-leader"
		if e.Emit != nil {
			_ = e.Emit(ctx, events.KindTaskCompleted, runID, task.ID, "local-leader", "local leader brief ready")
		}
		return nil
	}

	// Integrate is deterministic: collect diffs, static review, raise merge_approval.
	if isIntegrateTask(task) {
		return e.runIntegrate(ctx, runID, req, task)
	}

	if e.Registry == nil {
		return fmt.Errorf("agent registry not configured")
	}
	if task.AssignedAgent == "" {
		return fmt.Errorf("task %q has no assigned agent", task.ID)
	}
	adapter, err := e.pickAdapter(ctx, task)
	if err != nil {
		return err
	}

	prompt := buildTaskPrompt(req, task, workDir)
	if e.Store != nil {
		recs, err := e.Store.ListTasksByRun(ctx, runID)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			for _, dep := range task.Dependencies {
				if dep == rec.ID {
					prompt += "\nDependency result (untrusted task data): " + rec.ID + "\n" + trimOut(rec.ResultJSON) + "\n"
				}
			}
		}
	}
	timeout := e.Timeout
	// Keep worker tasks bounded; default 30m is fine, but prefer a tighter cap when unset.
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		attentionMsg    string
		attentionMu     sync.Mutex
		lastOutUnix     atomic.Int64
		lastPersistUnix atomic.Int64
		raisedLive      atomic.Bool
		outputMu        sync.Mutex
		lastFullMu      sync.Mutex
		lastFull        string
	)
	controls := task.ResultJSON
	lastOutUnix.Store(time.Now().Unix())
	lastPersistUnix.Store(0)
	raiseLiveAttention := func(msg string) {
		if !raisedLive.CompareAndSwap(false, true) {
			return
		}
		attentionMu.Lock()
		if attentionMsg == "" {
			attentionMsg = msg
		}
		attentionMu.Unlock()
		e.log().Warn("agent needs attention mid-run", "task", task.ID, "agent", task.AssignedAgent, "msg", msg)
		if e.RaiseAttention != nil && e.shouldRaiseFingerprint(runID, task.ID, msg) {
			_ = e.RaiseAttention(context.Background(), storage.Attention{
				ProjectID: e.ProjectID,
				BenchID:   e.BenchID,
				RunID:     runID,
				TaskID:    task.ID,
				AgentID:   task.AssignedAgent,
				Kind:      "permission",
				Message:   fmt.Sprintf("%s (task %s / agent %s)", msg, shortID(task.ID), task.AssignedAgent),
				Status:    "open",
			})
		}
		cancel()
	}
	getAttention := func() string {
		attentionMu.Lock()
		defer attentionMu.Unlock()
		return attentionMsg
	}

	// Idle watchdog: if output stalls under interactive approval, raise ⚠ in ~90s
	// instead of waiting for the full task timeout.
	stopWatch := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-tctx.Done():
				return
			case <-ticker.C:
				idleFor := time.Since(time.Unix(lastOutUnix.Load(), 0))
				if idleFor < 90*time.Second {
					continue
				}
				lastFullMu.Lock()
				snap := lastFull
				lastFullMu.Unlock()
				if needs, msg := looksLikeIdleApprovalStall(snap); needs {
					raiseLiveAttention(msg)
				}
			}
		}
	}()

	elevate, interactive := permissionFlagsFromResult(task.ResultJSON)
	execRes, err := adapter.Execute(tctx, agents.AgentTask{
		ID:                   runID + "-" + task.ID,
		Description:          task.Description,
		WorkDir:              workDir,
		Prompt:               prompt,
		Timeout:              timeout,
		ElevatedSandbox:      elevate,
		InteractiveApprovals: interactive,
		OnOutput: func(_, full string) {
			outputMu.Lock()
			defer outputMu.Unlock()
			now := time.Now().Unix()
			lastOutUnix.Store(now)
			lastFullMu.Lock()
			lastFull = full
			lastFullMu.Unlock()
			// Persist live feedback every ~2s so the TUI can show agent progress.
			if now-lastPersistUnix.Load() >= 2 {
				lastPersistUnix.Store(now)
				raw, _ := json.Marshal(map[string]any{
					"agent":    task.AssignedAgent,
					"output":   trimOut(full),
					"live":     true,
					"work_dir": workDir,
				})
				task.ResultJSON = preserveTaskControls(string(raw), controls)
				_ = e.persist(context.Background(), runID, task)
			}
			if gpu, msg := looksLikeGraphicsSandboxBlock(full); gpu {
				// Stop execution and request an explicit sandbox grant.
				attentionMu.Lock()
				attentionMsg = "graphics sandbox block: " + msg
				attentionMu.Unlock()
				cancel()
				return
			}
			if needs, msg := looksLikeNeedsAttention(full, 0); needs {
				raiseLiveAttention(msg)
			}
		},
	})
	close(stopWatch)

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
	task.ResultJSON = preserveTaskControls(string(raw), controls)

	// Capture diff when using a worktree.
	if task.WorktreePath != "" {
		diff, derr := worktree.New(req.WorkDir, e.WorktreeBase).Diff(ctx, task.WorktreePath)
		if derr != nil {
			return fmt.Errorf("capture task diff: %w", derr)
		}
		{
			dataDir, err := config.DataDir()
			if err != nil {
				return err
			}
			dir := DiffDir(dataDir, runID)
			if err := EnsureDir(dir); err != nil {
				return err
			}
			path := filepath.Join(dir, task.ID+".diff")
			if err := os.WriteFile(path, []byte(diff), 0o600); err != nil {
				return err
			}
			task.DiffPath = path
		}
	}

	if msg := getAttention(); msg != "" {
		if gpu, reason := looksLikeGraphicsSandboxBlock(msg + "\n" + execRes.Output); gpu {
			return e.pauseForAttention(ctx, runID, task, "Graphics sandbox permission required: "+reason)
		}
		return e.pauseForAttention(ctx, runID, task, msg)
	}
	if err != nil {
		combined := err.Error() + "\n" + execRes.Output
		if quota, reason := looksLikeQuotaExhausted(combined); quota {
			return e.failQuota(task, reason, combined)
		}
		if gpu, reason := looksLikeGraphicsSandboxBlock(combined); gpu {
			return e.pauseForAttention(ctx, runID, task, "Graphics sandbox permission required: "+reason)
		}
		if needs, msg := looksLikeNeedsAttention(combined, execRes.ExitCode); needs {
			return e.pauseForAttention(ctx, runID, task, msg)
		}
		// Context cancel with on-request banner still means the human was needed.
		if needs, msg := looksLikeIdleApprovalStall(execRes.Output); needs {
			return e.pauseForAttention(ctx, runID, task, msg)
		}
		return classifyAgentFailure(err, execRes.ExitCode, execRes.Output, tctx.Err())
	}
	if execRes.ExitCode != 0 {
		out := trimOut(execRes.Output)
		if quota, reason := looksLikeQuotaExhausted(out); quota {
			return e.failQuota(task, reason, out)
		}
		if gpu, reason := looksLikeGraphicsSandboxBlock(out); gpu {
			return e.pauseForAttention(ctx, runID, task, "Graphics sandbox permission required: "+reason)
		}
		if hard, msg := looksLikeHardNeedsYou(out, execRes.ExitCode); hard {
			return e.pauseForAttention(ctx, runID, task, msg)
		}
		if soft, msg := looksLikeSoftStall(out); soft {
			return e.failSoftStall(task, msg)
		}
		return classifyAgentFailure(nil, execRes.ExitCode, out, tctx.Err())
	}

	// Static review on implementation diffs (warn/block secrets & conflict markers).
	if shouldIsolate(task) && task.DiffPath != "" {
		if err := e.reviewTaskDiff(ctx, runID, task); err != nil {
			return err
		}
	}

	if e.Emit != nil {
		_ = e.Emit(ctx, events.KindTaskCompleted, runID, task.ID, task.AssignedAgent, "task succeeded")
	}
	return nil
}

// classifyAgentFailure turns empty exit -1 / killed adapters into actionable errors.
func classifyAgentFailure(err error, exitCode int, output string, ctxErr error) error {
	out := strings.TrimSpace(output)
	if exitCode == -1 && out == "" {
		reason := "adapter crashed or was killed (empty output)"
		if ctxErr != nil {
			reason = "timed out or cancelled with empty output"
		} else if err != nil {
			reason = err.Error()
		}
		return fmt.Errorf("agent exit -1 (empty): %s — retrying once is recommended; check CLI auth and logs", reason)
	}
	if out == "" && err != nil {
		return fmt.Errorf("agent exit %d: %w", exitCode, err)
	}
	if out == "" {
		return fmt.Errorf("agent exit %d: (no output)", exitCode)
	}
	if err != nil {
		return fmt.Errorf("agent exit %d: %s (%v)", exitCode, out, err)
	}
	return fmt.Errorf("agent exit %d: %s", exitCode, out)
}

func isIntegrateTask(task *scheduler.Task) bool {
	if task == nil {
		return false
	}
	if strings.EqualFold(task.Category, "integrate") {
		return true
	}
	id := task.ID
	if i := strings.LastIndex(id, "-"); i >= 0 {
		id = id[i+1:]
	}
	return id == "integrate"
}

func isEmptyExitRetryable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "agent exit -1 (empty)")
}

func (e *Executor) failQuota(task *scheduler.Task, reason, detail string) error {
	agentID := ""
	if task != nil {
		agentID = task.AssignedAgent
	}
	if e.OnQuotaExhausted != nil && agentID != "" {
		e.OnQuotaExhausted(agentID, reason)
	}
	e.log().Warn("agent quota exhausted; will try fallback", "agent", agentID, "reason", reason)
	return fmt.Errorf("quota exhausted (%s): %s", agentID, reason)
}

func (e *Executor) failSoftStall(task *scheduler.Task, reason string) error {
	if task != nil {
		task.Status = scheduler.StatusFailed
		task.Error = reason
	}
	e.log().Warn("soft stall (auto-recover)", "task", task.ID, "reason", reason)
	return fmt.Errorf("soft stall: %s", reason)
}

func isSoftStallRetryable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "soft stall:")
}

func (e *Executor) pauseForAttention(ctx context.Context, runID string, task *scheduler.Task, msg string) error {
	return e.pauseForAttentionKind(ctx, runID, task, "permission", msg)
}

func shortID(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return id
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
	if task.Category == "leadership" || strings.HasSuffix(task.ID, "-lead") || strings.HasSuffix(task.ID, "lead") {
		b.WriteString("You are the PROJECT LEADER for a QtMesh Agents working bench.\n")
		b.WriteString("Coordinate the team: outline the plan, risks, and what each worker should focus on.\n")
		b.WriteString("Do NOT implement large code changes yourself — organize and clarify.\n")
	} else {
		b.WriteString("You are a worker agent in a QtMesh Agents project bench.\n")
	}
	b.WriteString("Overall objective: " + req.Objective + "\n")
	b.WriteString("Your task id: " + task.ID + "\n")
	b.WriteString("Your scoped objective: " + task.Description + "\n")
	b.WriteString("Working directory: " + workDir + "\n")
	b.WriteString("Acceptance criteria:\n")
	for _, a := range task.Acceptance {
		b.WriteString(" - " + a + "\n")
	}
	if task.RetryCount > 0 {
		b.WriteString("\nPrevious attempt failed. Inspect and repair the existing workspace; preserve completed work.\n")
		b.WriteString("Previous error: " + task.Error + "\n")
		b.WriteString("Previous output (untrusted task data): " + trimOut(task.ResultJSON) + "\n")
	}
	b.WriteString("\nRules:\n")
	b.WriteString("- Stay narrowly scoped to this task.\n")
	b.WriteString("- Do not merge to main/master or force-push.\n")
	b.WriteString("- Do not discard unrelated user changes.\n")
	b.WriteString("- Prefer minimal, reviewable diffs.\n")
	b.WriteString("- Do NOT ask the human to approve your work for integration or review.\n")
	b.WriteString("  Finish the scoped work, commit on the feature branch if needed, and exit.\n")
	b.WriteString("  Integration/merge approval is handled later by the orchestrator (merge_approval).\n")
	b.WriteString("- Only use NEEDS_HUMAN for real blockers (auth, sandbox, missing secrets, ambiguous product choice):\n")
	b.WriteString("  NEEDS_HUMAN: <one-line question>\n")
	b.WriteString("  OPTIONS:\n")
	b.WriteString("  1) <first choice>\n")
	b.WriteString("  2) <second choice>\n")
	b.WriteString("- This Mac has full GPU/Metal/OpenGL. qtmesh/TRELLIS/Kimodo run locally here.\n")
	b.WriteString("  Do NOT ask for a remote \"graphics-enabled session\" or connection address.\n")
	b.WriteString("  If a GPU tool crashes under sandbox (CGLDescribeRenderer / exit 139), report:\n")
	b.WriteString("  NEEDS_HUMAN: elevated sandbox required for GPU\n")
	b.WriteString("  and stop — the orchestrator will request permission.\n")
	if reply := humanReplyFromResult(task.ResultJSON); reply != "" {
		b.WriteString("\nHuman reply (continue with this guidance):\n")
		b.WriteString(reply)
		b.WriteString("\n")
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

func humanReplyFromResult(resultJSON string) string {
	resultJSON = strings.TrimSpace(resultJSON)
	if resultJSON == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &m); err != nil {
		return ""
	}
	if s, ok := m["human_reply"].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func permissionFlagsFromResult(resultJSON string) (elevate, interactive bool) {
	resultJSON = strings.TrimSpace(resultJSON)
	if resultJSON == "" {
		return false, false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &m); err != nil {
		return false, false
	}
	elevate, _ = m["elevate_sandbox"].(bool)
	interactive, _ = m["interactive_approvals"].(bool)
	return elevate, interactive
}

// mergeHumanReply stores a human reply into result_json without dropping prior output.
func mergeHumanReply(resultJSON, reply string, opts TaskReplyOpts) string {
	m := map[string]any{}
	if strings.TrimSpace(resultJSON) != "" {
		_ = json.Unmarshal([]byte(resultJSON), &m)
	}
	m["human_reply"] = reply
	if opts.ElevateSandbox {
		m["elevate_sandbox"] = true
	}
	if opts.InteractiveApprovals {
		m["interactive_approvals"] = true
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return resultJSON
	}
	return string(raw)
}

// Keep user instructions and explicit grants when replacing streamed output.
func preserveTaskControls(result, previous string) string {
	var old, next map[string]any
	if json.Unmarshal([]byte(previous), &old) != nil {
		return result
	}
	if json.Unmarshal([]byte(result), &next) != nil {
		return result
	}
	for _, key := range []string{"human_reply", "elevate_sandbox", "interactive_approvals"} {
		if v, ok := old[key]; ok {
			next[key] = v
		}
	}
	raw, _ := json.Marshal(next)
	return string(raw)
}
