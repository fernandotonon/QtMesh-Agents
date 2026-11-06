package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/events"
	"github.com/fernandotonon/QtMesh-Agents/internal/router"
	"github.com/fernandotonon/QtMesh-Agents/internal/scheduler"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

const (
	RunPending         = "pending"
	RunPlanning        = "planning"
	RunRunning         = "running"
	RunSucceeded       = "succeeded"
	RunFailed          = "failed"
	RunCancelled       = "cancelled"
	RunNeedsAttention  = "needs_attention"
)

// PlanRequest is a natural-language objective.
type PlanRequest struct {
	Objective     string
	ProjectID     string
	WorkDir       string
	AgentOverride string // if set, assign every task to this agent
}

// Engine is the top-level autonomous orchestrator.
type Engine struct {
	cfg      config.Config
	store    *storage.Store
	registry *agents.Registry
	planner  Planner
	log      *slog.Logger

	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	running  map[string]bool
	procs    *procTracker

	// exhausted tracks vendor agents that hit quota/token limits this process.
	// PreferLeader / AskLeader / orch chat skip them until ClearExhausted or TTL.
	muExhaust sync.Mutex
	exhausted map[string]time.Time

	// OnAttentionResolved is invoked after an attention mark is resolved
	// (used to resume paused bench goals).
	OnAttentionResolved func(ctx context.Context, a storage.Attention)
}

// Dependencies for constructing an Engine.
type Deps struct {
	Config   config.Config
	Store    *storage.Store
	Registry *agents.Registry
	Planner  Planner
	Logger   *slog.Logger
}

func New(deps Deps) *Engine {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	planner := deps.Planner
	if planner == nil {
		planner = HeuristicPlanner{}
	}
	return &Engine{
		cfg:       deps.Config,
		store:     deps.Store,
		registry:  deps.Registry,
		planner:   planner,
		log:       log,
		cancels:   map[string]context.CancelFunc{},
		running:   map[string]bool{},
		procs:     newProcTracker(),
		exhausted: map[string]time.Time{},
	}
}

const agentExhaustTTL = 6 * time.Hour

// MarkAgentExhausted records that an agent hit quota/token limits.
func (e *Engine) MarkAgentExhausted(agentID, reason string) {
	if e == nil || agentID == "" {
		return
	}
	e.muExhaust.Lock()
	defer e.muExhaust.Unlock()
	if e.exhausted == nil {
		e.exhausted = map[string]time.Time{}
	}
	e.exhausted[agentID] = time.Now().UTC().Add(agentExhaustTTL)
	e.log.Warn("agent marked exhausted (quota/tokens)", "agent", agentID, "reason", reason, "until", e.exhausted[agentID])
}

// ClearAgentExhausted clears a prior quota mark (e.g. after the user tops up).
func (e *Engine) ClearAgentExhausted(agentID string) {
	if e == nil || agentID == "" {
		return
	}
	e.muExhaust.Lock()
	defer e.muExhaust.Unlock()
	delete(e.exhausted, agentID)
}

// IsAgentExhausted reports whether the agent is still within a quota cooldown.
func (e *Engine) IsAgentExhausted(agentID string) bool {
	if e == nil || agentID == "" {
		return false
	}
	e.muExhaust.Lock()
	defer e.muExhaust.Unlock()
	until, ok := e.exhausted[agentID]
	if !ok {
		return false
	}
	if time.Now().UTC().After(until) {
		delete(e.exhausted, agentID)
		return false
	}
	return true
}

// LeadershipOrder returns the configured leadership failover chain
// (default claude → cursor → codex).
func (e *Engine) LeadershipOrder() []string {
	if e != nil && len(e.cfg.Routing.LeadershipFallback) > 0 {
		return append([]string(nil), e.cfg.Routing.LeadershipFallback...)
	}
	return []string{"claude", "cursor", "codex"}
}

// OrchChatOrder returns the conversational orchestrator failover chain
// (cursor → claude → codex), skipping agents already marked exhausted.
func OrchChatOrder() []string {
	return []string{"cursor", "claude", "codex"}
}

// CreatePlan builds a validated plan (without executing).
func (e *Engine) CreatePlan(ctx context.Context, req PlanRequest) (scheduler.Plan, error) {
	if req.Objective == "" {
		return scheduler.Plan{}, fmt.Errorf("objective required")
	}
	planner := e.planner
	// Mock override always uses the deterministic planner for reliable local/CI runs.
	if req.AgentOverride == "mock" {
		planner = HeuristicPlanner{}
	}
	plan, err := planner.Plan(ctx, req)
	if err != nil {
		return scheduler.Plan{}, err
	}
	if req.AgentOverride != "" {
		plan = e.applyOverride(plan, req.AgentOverride)
	} else {
		plan = e.assignAgents(ctx, plan)
	}
	if err := plan.Validate(); err != nil {
		return scheduler.Plan{}, err
	}
	return plan, nil
}

func (e *Engine) assignAgents(ctx context.Context, plan scheduler.Plan) scheduler.Plan {
	available := map[string]bool{}
	if e.registry != nil {
		available = e.registry.DetectAll(ctx)
		for id := range available {
			entry, found := e.cfg.AgentByID(id)
			if !found || !entry.Enabled {
				available[id] = false
			}
			// Skip vendors already known to be out of tokens this process.
			if available[id] && e.IsAgentExhausted(id) {
				available[id] = false
			}
		}
	}
	r := router.New(e.cfg.Routing, available)
	for i := range plan.Tasks {
		t := &plan.Tasks[i]
		if t.AssignedAgent != "" {
			continue
		}
		// Leadership is executed locally — no vendor seat required.
		if t.Category == "leadership" || strings.HasSuffix(t.ID, "lead") {
			t.AssignedAgent = "local-leader"
			continue
		}
		cat := t.Category
		if cat == "" {
			cat = "implementation"
		}
		sel, err := r.Select(cat)
		if err != nil {
			for id, ok := range available {
				if ok {
					t.AssignedAgent = id
					break
				}
			}
			if t.AssignedAgent == "" && available["mock"] {
				t.AssignedAgent = "mock"
			}
			continue
		}
		t.AssignedAgent = sel.AgentID
	}
	return plan
}

func (e *Engine) applyOverride(plan scheduler.Plan, override string) scheduler.Plan {
	if override == "" {
		return plan
	}
	for i := range plan.Tasks {
		plan.Tasks[i].AssignedAgent = override
	}
	return plan
}

// StartRun plans and executes asynchronously. Returns the run ID immediately after planning.
func (e *Engine) StartRun(ctx context.Context, req PlanRequest) (string, error) {
	if req.Objective == "" {
		return "", fmt.Errorf("objective required")
	}
	if req.WorkDir == "" {
		return "", fmt.Errorf("work directory required (mention a registered project or pass work_dir)")
	}

	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	now := time.Now().UTC()

	var benchID string
	if req.ProjectID != "" {
		workers := []string{}
		if req.AgentOverride != "" {
			workers = append(workers, req.AgentOverride)
		}
		if b, berr := e.EnsureBench(ctx, req.ProjectID, req.ProjectID, workers); berr == nil {
			benchID = b.ID
		}
	}

	run := storage.Run{
		ID:        runID,
		ProjectID: req.ProjectID,
		BenchID:   benchID,
		Objective: req.Objective,
		Status:    RunPlanning,
		Planner:   e.planner.Name(),
		WorkDir:   req.WorkDir,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := e.store.UpsertRun(ctx, run); err != nil {
		return "", err
	}
	_ = e.emit(ctx, events.KindTaskQueued, runID, "", "", "run planning started")

	plan, err := e.CreatePlan(ctx, req)
	if err != nil {
		run.Status = RunFailed
		run.Error = err.Error()
		ended := time.Now().UTC()
		run.EndedAt = &ended
		_ = e.store.UpsertRun(ctx, run)
		return runID, err
	}
	plan = uniquifyTaskIDs(runID, plan)

	for _, t := range plan.Tasks {
		rec := taskToRecord(runID, t)
		if err := e.store.UpsertTask(ctx, rec); err != nil {
			return runID, err
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	// Register as live BEFORE persisting "running" so a concurrent resume/sweep
	// cannot treat this run as an orphan mid-start.
	e.mu.Lock()
	e.cancels[runID] = cancel
	e.running[runID] = true
	e.mu.Unlock()

	run.Status = RunRunning
	run.UpdatedAt = time.Now().UTC()
	if err := e.store.UpsertRun(ctx, run); err != nil {
		cancel()
		e.mu.Lock()
		delete(e.cancels, runID)
		delete(e.running, runID)
		e.mu.Unlock()
		return runID, err
	}

	go e.executeRun(runCtx, runID, req, plan)
	return runID, nil
}

// StartUnitRun plans a focused impl→verify unit and executes asynchronously.
// Used by the bench goal loop so one DAG failure does not end the goal.
func (e *Engine) StartUnitRun(ctx context.Context, req PlanRequest) (string, error) {
	if req.Objective == "" {
		return "", fmt.Errorf("objective required")
	}
	if req.WorkDir == "" {
		return "", fmt.Errorf("work directory required")
	}

	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	now := time.Now().UTC()

	var benchID string
	if req.ProjectID != "" {
		workers := []string{}
		if req.AgentOverride != "" {
			workers = append(workers, req.AgentOverride)
		}
		if b, berr := e.EnsureBench(ctx, req.ProjectID, req.ProjectID, workers); berr == nil {
			benchID = b.ID
		}
	}

	run := storage.Run{
		ID: runID, ProjectID: req.ProjectID, BenchID: benchID,
		Objective: req.Objective, Status: RunPlanning, Planner: "unit",
		WorkDir: req.WorkDir, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.store.UpsertRun(ctx, run); err != nil {
		return "", err
	}

	plan, err := UnitPlan(req)
	if err != nil {
		run.Status = RunFailed
		run.Error = err.Error()
		ended := time.Now().UTC()
		run.EndedAt = &ended
		_ = e.store.UpsertRun(ctx, run)
		return runID, err
	}
	// Same seating as CreatePlan — UnitPlan itself leaves AssignedAgent empty.
	if req.AgentOverride != "" {
		plan = e.applyOverride(plan, req.AgentOverride)
	} else {
		plan = e.assignAgents(ctx, plan)
	}
	plan = uniquifyTaskIDs(runID, plan)
	for _, t := range plan.Tasks {
		if err := e.store.UpsertTask(ctx, taskToRecord(runID, t)); err != nil {
			return runID, err
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.cancels[runID] = cancel
	e.running[runID] = true
	e.mu.Unlock()

	run.Status = RunRunning
	run.UpdatedAt = time.Now().UTC()
	if err := e.store.UpsertRun(ctx, run); err != nil {
		cancel()
		e.mu.Lock()
		delete(e.cancels, runID)
		delete(e.running, runID)
		e.mu.Unlock()
		return runID, err
	}
	go e.executeRun(runCtx, runID, req, plan)
	return runID, nil
}

// WaitRun blocks until the run reaches a terminal status (or ctx cancels).
func (e *Engine) WaitRun(ctx context.Context, runID string) (storage.Run, error) {
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		r, err := e.store.GetRun(ctx, runID)
		if err != nil {
			return storage.Run{}, err
		}
		switch r.Status {
		case RunSucceeded, RunFailed, RunCancelled, RunNeedsAttention:
			return r, nil
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-ticker.C:
		}
	}
}

// CancelRun cancels a running orchestration. Always updates storage and reaps
// leftover worker processes matching the run id (even after daemon restart).
func (e *Engine) CancelRun(runID string) error {
	e.mu.Lock()
	cancel, ok := e.cancels[runID]
	e.mu.Unlock()
	if ok {
		cancel()
	}
	if e.procs != nil {
		e.procs.killRun(runID)
	}
	// Best-effort: kill orphaned vendor CLIs still carrying this run id.
	killOrphanWorkers(runID)

	ctx := context.Background()
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		if ok {
			return nil // cancel signaled even if store lookup failed
		}
		return fmt.Errorf("run %q not found: %w", runID, err)
	}
	switch run.Status {
	case RunSucceeded, RunFailed, RunCancelled:
		return nil
	}
	ended := time.Now().UTC()
	run.Status = RunCancelled
	if ok {
		run.Error = "cancelled"
	} else {
		run.Error = "cancelled (orphaned — no active executor)"
	}
	run.EndedAt = &ended
	run.UpdatedAt = ended
	if err := e.store.UpsertRun(ctx, run); err != nil {
		return err
	}
	tasks, _ := e.store.ListTasksByRun(ctx, runID)
	for _, t := range tasks {
		switch t.Status {
		case "running", "pending", "ready", "awaiting_input":
			t.Status = "cancelled"
			t.Error = "cancelled"
			t.UpdatedAt = ended
			_ = e.store.UpsertTask(ctx, t)
		}
	}
	return nil
}

// TaskReplyOpts controls how a paused task is resumed after a human reply.
type TaskReplyOpts struct {
	ElevateSandbox       bool
	InteractiveApprovals bool
}

// ReplyToTask records a human reply for a task waiting on input and resumes the run.
func (e *Engine) ReplyToTask(ctx context.Context, runID, taskID, reply string, opts TaskReplyOpts) error {
	reply = strings.TrimSpace(reply)
	if runID == "" || taskID == "" || reply == "" {
		return fmt.Errorf("run_id, task_id, and reply are required")
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	task, err := e.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.RunID != "" && task.RunID != runID {
		return fmt.Errorf("task %q does not belong to run %q", taskID, runID)
	}
	switch task.Status {
	case "awaiting_input", "failed":
	default:
		return fmt.Errorf("task %q is %s (expected awaiting_input)", taskID, task.Status)
	}

	now := time.Now().UTC()
	task.ResultJSON = mergeHumanReply(task.ResultJSON, reply, opts)
	task.Status = "pending"
	task.Error = ""
	task.UpdatedAt = now
	if err := e.store.UpsertTask(ctx, task); err != nil {
		return err
	}

	// Resolve open attention marks for this task/run (also heals bench ⚠ status).
	open, _ := e.store.ListOpenAttention(ctx)
	for _, a := range open {
		if a.RunID == runID && (a.TaskID == "" || a.TaskID == taskID) {
			_ = e.ResolveAttention(ctx, a.ID)
		}
	}
	// Ensure bench is not left needs_attention after replies cleared the marks.
	_ = e.healBenchAttention(ctx, run.ProjectID, "")

	e.mu.Lock()
	already := e.running[runID]
	e.mu.Unlock()
	if already {
		return fmt.Errorf("run %s is still executing — wait until it pauses on ⚠, then reply", runID)
	}

	run.Status = RunRunning
	run.Error = ""
	run.UpdatedAt = now
	run.EndedAt = nil
	_ = e.store.UpsertRun(ctx, run)
	return e.resumeAfterReply(ctx, runID, taskID, reply, opts)
}

func (e *Engine) resumeAfterReply(ctx context.Context, runID, taskID, reply string, opts TaskReplyOpts) error {
	r, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	recs, err := e.store.ListTasksByRun(ctx, runID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	tasks := make([]scheduler.Task, 0, len(recs))
	for _, rec := range recs {
		st := scheduler.TaskStatus(rec.Status)
		if rec.ID == taskID {
			st = scheduler.StatusPending
			rec.Status = string(st)
			rec.ResultJSON = mergeHumanReply(rec.ResultJSON, reply, opts)
			rec.Error = ""
			rec.UpdatedAt = now
			_ = e.store.UpsertTask(ctx, rec)
		} else if st == scheduler.StatusAwaitingInput || st == scheduler.StatusBlocked {
			// Unstick paused/orphaned downstream tasks so the DAG can continue.
			st = scheduler.StatusPending
			rec.Status = string(st)
			rec.Error = "resumed after human reply"
			rec.UpdatedAt = now
			_ = e.store.UpsertTask(ctx, rec)
		}
		tasks = append(tasks, scheduler.Task{
			ID: rec.ID, Description: rec.Description, Category: rec.Category,
			Dependencies: rec.Dependencies, AssignedAgent: rec.AssignedAgent,
			Status: st, Acceptance: rec.Acceptance, MaxRetries: rec.MaxRetries,
			RetryCount: rec.RetryCount, ProjectID: rec.ProjectID, RunID: r.ID,
			WorktreePath: rec.WorktreePath, Branch: rec.Branch,
			Error: rec.Error, DiffPath: rec.DiffPath, ResultJSON: rec.ResultJSON,
		})
	}

	r.Status = RunRunning
	r.Error = ""
	r.UpdatedAt = now
	r.EndedAt = nil
	_ = e.store.UpsertRun(ctx, r)

	req := PlanRequest{Objective: r.Objective, ProjectID: r.ProjectID, WorkDir: r.WorkDir}
	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.cancels[runID] = cancel
	e.running[runID] = true
	e.mu.Unlock()
	e.log.Info("resuming run after human reply", "run", runID, "task", taskID)
	go e.executeRun(runCtx, runID, req, scheduler.Plan{Tasks: tasks})
	return nil
}

// ResumeIncompleteRun unsticks a failed/needs_attention run whose downstream
// tasks were orphaned as "blocked" (e.g. after a mid-run ⚠ pause) and continues
// the DAG from remaining pending work. Succeeded tasks are left alone.
func (e *Engine) ResumeIncompleteRun(ctx context.Context, runID string) error {
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	r, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	switch r.Status {
	case RunFailed, RunNeedsAttention, RunCancelled:
	case RunRunning, RunPlanning:
		e.mu.Lock()
		already := e.running[runID]
		e.mu.Unlock()
		if already {
			return fmt.Errorf("run %s is still executing", runID)
		}
	default:
		return fmt.Errorf("run %s is %s — nothing to resume", runID, r.Status)
	}
	recs, err := e.store.ListTasksByRun(ctx, runID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	tasks := make([]scheduler.Task, 0, len(recs))
	unstuck := 0
	for _, rec := range recs {
		st := scheduler.TaskStatus(rec.Status)
		switch st {
		case scheduler.StatusBlocked, scheduler.StatusAwaitingInput, scheduler.StatusCancelled:
			st = scheduler.StatusPending
			rec.Status = string(st)
			rec.Error = "resumed incomplete run"
			rec.UpdatedAt = now
			_ = e.store.UpsertTask(ctx, rec)
			unstuck++
		case scheduler.StatusFailed:
			// Retry failed verify/integrate; leave failed impl alone if it has a
			// worktree commit — re-running expensive generation is rarely wanted.
			id := shortID(rec.ID)
			if id == "verify" || id == "integrate" || strings.HasSuffix(rec.ID, "-verify") || strings.HasSuffix(rec.ID, "-integrate") {
				st = scheduler.StatusPending
				rec.Status = string(st)
				rec.Error = "retry after incomplete run"
				rec.UpdatedAt = now
				_ = e.store.UpsertTask(ctx, rec)
				unstuck++
			}
		}
		tasks = append(tasks, scheduler.Task{
			ID: rec.ID, Description: rec.Description, Category: rec.Category,
			Dependencies: rec.Dependencies, AssignedAgent: rec.AssignedAgent,
			Status: st, Acceptance: rec.Acceptance, MaxRetries: rec.MaxRetries,
			RetryCount: rec.RetryCount, ProjectID: rec.ProjectID, RunID: r.ID,
			WorktreePath: rec.WorktreePath, Branch: rec.Branch,
			Error: rec.Error, DiffPath: rec.DiffPath, ResultJSON: rec.ResultJSON,
		})
	}
	if unstuck == 0 && scheduler.Succeeded(tasks) {
		return fmt.Errorf("run %s already complete", runID)
	}
	if scheduler.AllTerminal(tasks) && !scheduler.NeedsAttention(tasks) {
		// Nothing runnable even after unstick.
		pending := false
		for _, t := range tasks {
			if t.Status == scheduler.StatusPending || t.Status == scheduler.StatusReady {
				pending = true
				break
			}
		}
		if !pending {
			return fmt.Errorf("run %s has no remaining work to resume", runID)
		}
	}

	r.Status = RunRunning
	r.Error = ""
	r.UpdatedAt = now
	r.EndedAt = nil
	_ = e.store.UpsertRun(ctx, r)

	req := PlanRequest{Objective: r.Objective, ProjectID: r.ProjectID, WorkDir: r.WorkDir}
	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.cancels[runID] = cancel
	e.running[runID] = true
	e.mu.Unlock()
	e.log.Info("resuming incomplete run", "run", runID, "unstuck", unstuck)
	go e.executeRun(runCtx, runID, req, scheduler.Plan{Tasks: tasks})
	return nil
}

// LatestIncompleteRunID returns the newest failed/needs_attention run for a project
// that still has blocked/pending/awaiting work after some successes.
func (e *Engine) LatestIncompleteRunID(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("project id required")
	}
	list, err := e.store.ListRuns(ctx, 40)
	if err != nil {
		return "", err
	}
	for _, r := range list {
		if r.ProjectID != projectID {
			continue
		}
		switch r.Status {
		case RunFailed, RunNeedsAttention, RunCancelled:
		default:
			continue
		}
		recs, err := e.store.ListTasksByRun(ctx, r.ID)
		if err != nil {
			continue
		}
		hasSuccess, hasRemaining := false, false
		for _, t := range recs {
			switch t.Status {
			case string(scheduler.StatusSucceeded):
				hasSuccess = true
			case string(scheduler.StatusBlocked), string(scheduler.StatusPending),
				string(scheduler.StatusReady), string(scheduler.StatusAwaitingInput),
				string(scheduler.StatusFailed):
				id := shortID(t.ID)
				if t.Status == string(scheduler.StatusFailed) && id != "verify" && id != "integrate" &&
					!strings.HasSuffix(t.ID, "-verify") && !strings.HasSuffix(t.ID, "-integrate") {
					continue
				}
				hasRemaining = true
			}
		}
		if hasSuccess && hasRemaining {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("no incomplete run for %s", projectID)
}

// ResumeOrphanedRuns re-attaches executors for runs left "running" after a daemon restart.
// Interrupted in-flight tasks are reset to pending so the DAG can continue.
func (e *Engine) ResumeOrphanedRuns(ctx context.Context) (int, error) {
	list, err := e.store.ListRuns(ctx, 50)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range list {
		switch r.Status {
		case RunRunning, RunPlanning, RunNeedsAttention:
		default:
			continue
		}
		e.mu.Lock()
		already := e.running[r.ID]
		e.mu.Unlock()
		if already {
			continue
		}
		if err := e.resumeRun(ctx, r); err != nil {
			e.log.Warn("resume run", "run", r.ID, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

func (e *Engine) resumeRun(ctx context.Context, r storage.Run) error {
	recs, err := e.store.ListTasksByRun(ctx, r.ID)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("no tasks")
	}
	now := time.Now().UTC()
	// Skip brand-new runs — another StartRun may still be wiring the executor.
	if now.Sub(r.UpdatedAt) < 15*time.Second {
		return fmt.Errorf("too fresh to resume")
	}
	tasks := make([]scheduler.Task, 0, len(recs))
	for _, rec := range recs {
		st := scheduler.TaskStatus(rec.Status)
		switch st {
		case scheduler.StatusAwaitingInput:
			// Keep paused — human must reply (auth / permission). Do not wipe the stall.
		case scheduler.StatusRunning:
			// Interrupted mid-flight — retry.
			st = scheduler.StatusPending
			rec.Status = string(st)
			rec.Error = "resumed after daemon restart"
			rec.UpdatedAt = now
			_ = e.store.UpsertTask(ctx, rec)
		case scheduler.StatusCancelled:
			// Keep cancelled — user/sweep stopped this task.
		}
		tasks = append(tasks, scheduler.Task{
			ID: rec.ID, Description: rec.Description, Category: rec.Category,
			Dependencies: rec.Dependencies, AssignedAgent: rec.AssignedAgent,
			Status: st, Acceptance: rec.Acceptance, MaxRetries: rec.MaxRetries,
			RetryCount: rec.RetryCount, ProjectID: rec.ProjectID, RunID: r.ID,
			WorktreePath: rec.WorktreePath, Branch: rec.Branch,
			Error: rec.Error, DiffPath: rec.DiffPath, ResultJSON: rec.ResultJSON,
		})
	}
	// If everything is already terminal, just reconcile run status.
	if scheduler.AllTerminal(tasks) {
		return nil
	}
	r.Status = RunRunning
	r.Error = ""
	r.UpdatedAt = now
	r.EndedAt = nil
	_ = e.store.UpsertRun(ctx, r)

	req := PlanRequest{
		Objective: r.Objective,
		ProjectID: r.ProjectID,
		WorkDir:   r.WorkDir,
	}
	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.cancels[r.ID] = cancel
	e.running[r.ID] = true
	e.mu.Unlock()
	e.log.Info("resuming orphaned run", "run", r.ID)
	go e.executeRun(runCtx, r.ID, req, scheduler.Plan{Tasks: tasks})
	return nil
}

// SweepStaleTasks fails tasks that have been "running" too long under a live executor.
func (e *Engine) SweepStaleTasks(ctx context.Context, maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		maxAge = 25 * time.Minute
	}
	list, err := e.store.ListRuns(ctx, 50)
	if err != nil {
		return 0, err
	}
	n := 0
	now := time.Now().UTC()
	e.mu.Lock()
	live := make(map[string]bool, len(e.running))
	for id := range e.running {
		live[id] = true
	}
	e.mu.Unlock()

	for _, r := range list {
		if !live[r.ID] {
			continue
		}
		tasks, err := e.store.ListTasksByRun(ctx, r.ID)
		if err != nil {
			continue
		}
		for _, t := range tasks {
			if t.Status != "running" {
				continue
			}
			if now.Sub(t.UpdatedAt) < maxAge {
				continue
			}
			t.Status = "failed"
			t.Error = fmt.Sprintf("stale: running longer than %s without completion", maxAge)
			t.UpdatedAt = now
			_ = e.store.UpsertTask(ctx, t)
			n++
			_ = e.RaiseAttention(ctx, storage.Attention{
				ProjectID: r.ProjectID, BenchID: r.BenchID, RunID: r.ID, TaskID: t.ID,
				AgentID: t.AssignedAgent, Kind: "stalled",
				Message: fmt.Sprintf("Task %s stalled — marked failed after %s", t.ID, maxAge),
				Status:  "open",
			})
		}
	}
	return n, nil
}

// CancelActiveRunsForProject cancels every non-terminal run for a project.
// Used when the user asks to continue/supersede work so agents are not piled up.
func (e *Engine) CancelActiveRunsForProject(ctx context.Context, projectID string) ([]string, error) {
	list, err := e.store.ListRuns(ctx, 50)
	if err != nil {
		return nil, err
	}
	var cancelled []string
	for _, r := range list {
		if projectID != "" && r.ProjectID != projectID {
			continue
		}
		switch r.Status {
		case RunPending, RunPlanning, RunRunning:
			if err := e.CancelRun(r.ID); err != nil {
				e.log.Warn("cancel run", "run", r.ID, "err", err)
				continue
			}
			cancelled = append(cancelled, r.ID)
		}
	}
	return cancelled, nil
}

func (e *Engine) executeRun(ctx context.Context, runID string, req PlanRequest, plan scheduler.Plan) {
	defer func() {
		e.mu.Lock()
		delete(e.cancels, runID)
		delete(e.running, runID)
		e.mu.Unlock()
	}()

	maxConc := e.cfg.Execution.MaxConcurrentAgents
	if maxConc < 1 {
		maxConc = 1
	}
	timeout := time.Duration(e.cfg.Execution.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}

	benchID, _ := e.store.GetRunBenchID(context.Background(), runID)
	exec := &Executor{
		Registry:        e.registry,
		Store:           e.store,
		Log:             e.log,
		UseWorktrees:    e.cfg.Execution.UseGitWorktrees,
		WorktreeBase:    filepath.Join(req.WorkDir, ".qma-worktrees"),
		Timeout:         timeout,
		MaxRetries:      e.cfg.Execution.MaxRetries,
		RequireApproval: e.cfg.Orchestrator.RequireApprovalForMerge,
		RaiseAttention:  e.RaiseAttention,
		OnQuotaExhausted: func(agentID, reason string) {
			e.MarkAgentExhausted(agentID, reason)
			if req.ProjectID != "" {
				_, _ = e.ReseatLeaderForProject(context.Background(), req.ProjectID, reason)
			}
		},
		IsExhausted: e.IsAgentExhausted,
		ProjectID:   req.ProjectID,
		BenchID:     benchID,
		Emit:        e.emit,
	}
	if req.AgentOverride != "" {
		// Honor forced agent — do not silently hop to another vendor mid-run.
		exec.SetFallbackAgents(nil)
	} else {
		exec.SetFallbackAgents(e.cfg.Routing.Fallback)
	}

	err := scheduler.RunDAG(ctx, plan.Tasks, maxConc, func(ctx context.Context, task *scheduler.Task) error {
		return exec.RunTask(ctx, runID, req, task)
	})

	// Persist final in-memory task states (covers blocked/cancelled transitions).
	bg := context.Background()
	for i := range plan.Tasks {
		_ = e.store.UpsertTask(bg, taskToRecord(runID, plan.Tasks[i]))
	}

	run, getErr := e.store.GetRun(bg, runID)
	if getErr != nil {
		e.log.Error("load run after execute", "err", getErr)
		return
	}
	ended := time.Now().UTC()
	run.EndedAt = &ended
	run.UpdatedAt = ended

	tasks, _ := e.store.ListTasksByRun(bg, runID)
	if ctx.Err() != nil {
		run.Status = RunCancelled
		run.Error = "cancelled"
		run.Summary = summarizeTasks(tasks)
	} else if anyAwaitingInput(tasks) {
		run.Status = RunNeedsAttention
		run.Error = "one or more tasks need your attention"
		run.Summary = summarizeTasks(tasks)
		_ = e.RaiseAttention(bg, storage.Attention{
			ProjectID: run.ProjectID, BenchID: run.BenchID, RunID: runID,
			Kind: "permission", Message: "Run paused — open the tree (⚠) and resolve attention marks.",
			Status: "open", AgentID: firstAwaitingAgent(tasks),
			TaskID: firstAwaitingTask(tasks),
		})
	} else if err != nil {
		run.Status = RunFailed
		run.Error = err.Error()
		run.Summary = summarizeTasks(tasks)
	} else if allSucceeded(tasks) {
		run.Status = RunSucceeded
		run.Summary = summarizeTasks(tasks)
		// Integrate already raised merge_approval (and applied on approve). Do not re-raise.
	} else {
		run.Status = RunFailed
		run.Summary = summarizeTasks(tasks)
		run.Error = "one or more tasks failed"
	}
	_ = e.store.UpsertRun(bg, run)
	_ = e.persistRunMetric(bg, run, tasks)
	_ = e.emit(bg, events.KindTaskCompleted, runID, "", "", "run finished: "+run.Status)
}

func (e *Engine) persistRunMetric(ctx context.Context, run storage.Run, tasks []storage.TaskRecord) error {
	var retries int
	agent := ""
	for _, t := range tasks {
		retries += t.RetryCount
		if agent == "" && t.AssignedAgent != "" && t.AssignedAgent != "local-leader" {
			agent = t.AssignedAgent
		}
	}
	dur := int64(0)
	if run.EndedAt != nil && !run.CreatedAt.IsZero() {
		dur = run.EndedAt.Sub(run.CreatedAt).Milliseconds()
	}
	return e.store.UpsertRunMetric(ctx, storage.RunMetric{
		ID:         "met-" + run.ID,
		RunID:      run.ID,
		ProjectID:  run.ProjectID,
		AgentID:    agent,
		DurationMS: dur,
		Retries:    retries,
		Success:    run.Status == RunSucceeded,
	})
}

func (e *Engine) emit(ctx context.Context, kind events.Kind, runID, taskID, agentID, msg string) error {
	ev := events.New(kind, msg)
	ev.ID = fmt.Sprintf("evt-%d", time.Now().UnixNano())
	ev.TaskID = taskID
	ev.AgentID = agentID
	ev.Data["run_id"] = runID
	return e.store.InsertEvent(ctx, ev.ID, string(ev.Kind), ev.Timestamp.Format(time.RFC3339),
		"", "", agentID, taskID, msg, fmt.Sprintf(`{"run_id":%q}`, runID))
}

// uniquifyTaskIDs prefixes task IDs with the run ID so concurrent/sequential
// runs do not collide in SQLite (task id is the primary key).
func uniquifyTaskIDs(runID string, plan scheduler.Plan) scheduler.Plan {
	idMap := make(map[string]string, len(plan.Tasks))
	for _, t := range plan.Tasks {
		if strings.HasPrefix(t.ID, runID+"-") {
			idMap[t.ID] = t.ID
			continue
		}
		idMap[t.ID] = runID + "-" + t.ID
	}
	for i := range plan.Tasks {
		t := &plan.Tasks[i]
		t.ID = idMap[t.ID]
		deps := make([]string, len(t.Dependencies))
		for j, d := range t.Dependencies {
			if neu, ok := idMap[d]; ok {
				deps[j] = neu
			} else {
				deps[j] = d
			}
		}
		t.Dependencies = deps
	}
	return plan
}

func taskToRecord(runID string, t scheduler.Task) storage.TaskRecord {
	return storage.TaskRecord{
		ID:            t.ID,
		RunID:         runID,
		ProjectID:     t.ProjectID,
		Description:   t.Description,
		Category:      t.Category,
		Status:        string(t.Status),
		AssignedAgent: t.AssignedAgent,
		Acceptance:    t.Acceptance,
		Dependencies:  t.Dependencies,
		MaxRetries:    t.MaxRetries,
		RetryCount:    t.RetryCount,
		WorktreePath:  t.WorktreePath,
		Branch:        t.Branch,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
		ResultJSON:    t.ResultJSON,
		Error:         t.Error,
		DiffPath:      t.DiffPath,
	}
}

func summarizeTasks(tasks []storage.TaskRecord) string {
	var ok, fail, other int
	for _, t := range tasks {
		switch t.Status {
		case string(scheduler.StatusSucceeded):
			ok++
		case string(scheduler.StatusFailed):
			fail++
		default:
			other++
		}
	}
	return fmt.Sprintf("%d succeeded, %d failed, %d other", ok, fail, other)
}

func allSucceeded(tasks []storage.TaskRecord) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		if t.Status != string(scheduler.StatusSucceeded) {
			return false
		}
	}
	return true
}

func anyAwaitingInput(tasks []storage.TaskRecord) bool {
	for _, t := range tasks {
		if t.Status == string(scheduler.StatusAwaitingInput) {
			return true
		}
	}
	return false
}

func firstAwaitingTask(tasks []storage.TaskRecord) string {
	for _, t := range tasks {
		if t.Status == string(scheduler.StatusAwaitingInput) {
			return t.ID
		}
	}
	return ""
}

func firstAwaitingAgent(tasks []storage.TaskRecord) string {
	for _, t := range tasks {
		if t.Status == string(scheduler.StatusAwaitingInput) {
			return t.AssignedAgent
		}
	}
	return ""
}

// DiffDir returns the directory where task diffs are stored.
func DiffDir(dataDir, runID string) string {
	return filepath.Join(dataDir, "diffs", runID)
}

// EnsureDir is a small helper.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o700)
}
