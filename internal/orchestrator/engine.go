package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	RunPending   = "pending"
	RunPlanning  = "planning"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
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
		cfg:      deps.Config,
		store:    deps.Store,
		registry: deps.Registry,
		planner:  planner,
		log:      log,
		cancels:  map[string]context.CancelFunc{},
		running:  map[string]bool{},
	}
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
		}
	}
	r := router.New(e.cfg.Routing, available)
	for i := range plan.Tasks {
		t := &plan.Tasks[i]
		if t.AssignedAgent != "" {
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
	run := storage.Run{
		ID:        runID,
		ProjectID: req.ProjectID,
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

	for _, t := range plan.Tasks {
		rec := taskToRecord(runID, t)
		if err := e.store.UpsertTask(ctx, rec); err != nil {
			return runID, err
		}
	}

	run.Status = RunRunning
	run.UpdatedAt = time.Now().UTC()
	if err := e.store.UpsertRun(ctx, run); err != nil {
		return runID, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.cancels[runID] = cancel
	e.running[runID] = true
	e.mu.Unlock()

	go e.executeRun(runCtx, runID, req, plan)
	return runID, nil
}

// CancelRun cancels a running orchestration.
func (e *Engine) CancelRun(runID string) error {
	e.mu.Lock()
	cancel, ok := e.cancels[runID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %q not active", runID)
	}
	cancel()
	return nil
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

	exec := &Executor{
		Registry:        e.registry,
		Store:           e.store,
		Log:             e.log,
		UseWorktrees:    e.cfg.Execution.UseGitWorktrees,
		WorktreeBase:    filepath.Join(req.WorkDir, ".qma-worktrees"),
		Timeout:         timeout,
		MaxRetries:      e.cfg.Execution.MaxRetries,
		RequireApproval: e.cfg.Orchestrator.RequireApprovalForMerge,
		Emit:            e.emit,
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
	} else if err != nil {
		run.Status = RunFailed
		run.Error = err.Error()
		run.Summary = summarizeTasks(tasks)
	} else if allSucceeded(tasks) {
		run.Status = RunSucceeded
		run.Summary = summarizeTasks(tasks)
		if e.cfg.Orchestrator.RequireApprovalForMerge {
			run.Summary += "\nMerge not performed — approval required."
		}
	} else {
		run.Status = RunFailed
		run.Summary = summarizeTasks(tasks)
		run.Error = "one or more tasks failed"
	}
	_ = e.store.UpsertRun(bg, run)
	_ = e.emit(bg, events.KindTaskCompleted, runID, "", "", "run finished: "+run.Status)
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

// DiffDir returns the directory where task diffs are stored.
func DiffDir(dataDir, runID string) string {
	return filepath.Join(dataDir, "diffs", runID)
}

// EnsureDir is a small helper.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o700)
}
