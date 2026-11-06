package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

const (
	benchGoalMaxIters   = 12
	benchGoalWallClock  = 90 * time.Minute
	benchGoalMsgLimit   = 30
)

type leaderUnitDecision struct {
	Done           bool   `json:"done"`
	Summary        string `json:"summary"`
	NextObjective  string `json:"next_objective"`
	NeedsYou       bool   `json:"needs_you"`
	NeedsYouReason string `json:"needs_you_reason"`
}

// goalLoopState tracks in-process bench goal loops.
type goalLoopState struct {
	mu    sync.Mutex
	alive map[string]context.CancelFunc // goalID → cancel
}

func (c *Conductor) goalLoops() *goalLoopState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loops == nil {
		c.loops = &goalLoopState{alive: map[string]context.CancelFunc{}}
	}
	return c.loops
}

// StartBenchGoal opens (or reuses) the project bench thread and starts the
// durable goal loop. Returns the goal id.
func (c *Conductor) StartBenchGoal(ctx context.Context, projectID, objective, forceAgent string) (string, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return "", fmt.Errorf("objective required")
	}
	if c.engine == nil || c.engine.store == nil {
		return "", fmt.Errorf("engine not configured")
	}
	projs, err := c.projects.List(ctx)
	if err != nil {
		return "", err
	}
	refs := refsFromStorage(projs)
	ref, ok := findProject(refs, projectID)
	if !ok {
		return "", fmt.Errorf("unknown project %q", projectID)
	}

	bench, err := c.engine.EnsureBench(ctx, ref.ID, ref.Name, nil)
	if err != nil {
		return "", err
	}

	// Cancel any prior active goal on this project (one active goal per project).
	if prev, err := c.engine.store.GetActiveBenchGoal(ctx, ref.ID); err == nil && prev.ID != "" {
		_ = c.CancelBenchGoal(ctx, prev.ID, "superseded by new goal")
	}

	thread, err := c.ensureBenchThread(ctx, bench, ref.ID)
	if err != nil {
		return "", err
	}

	goalID := fmt.Sprintf("goal-%d", time.Now().UnixNano())
	goal := storage.BenchGoal{
		ID: goalID, BenchID: bench.ID, ProjectID: ref.ID, ThreadID: thread.ID,
		Objective: objective, Status: storage.GoalActive, Iteration: 0,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := c.engine.store.UpsertBenchGoal(ctx, goal); err != nil {
		return "", err
	}
	_ = c.appendThread(ctx, thread.ID, storage.ThreadRoleUser, "", objective)

	c.startGoalLoop(goalID, ref, forceAgent)
	return goalID, nil
}

// AmendBenchGoal rewrites the objective of an active/paused goal, appends the
// user correction to the thread, and cancels any in-flight unit so the next
// loop iteration follows the new objective (same goal ID — no supersede).
func (c *Conductor) AmendBenchGoal(ctx context.Context, goalID, newObjective, userNote string) error {
	newObjective = strings.TrimSpace(newObjective)
	if newObjective == "" {
		return fmt.Errorf("objective required")
	}
	if c.engine == nil || c.engine.store == nil {
		return fmt.Errorf("engine not configured")
	}
	goal, err := c.engine.store.GetBenchGoal(ctx, goalID)
	if err != nil {
		return err
	}
	switch goal.Status {
	case storage.GoalSucceeded, storage.GoalFailed, storage.GoalCancelled:
		return fmt.Errorf("goal %s is %s", goalID, goal.Status)
	}
	goal.Objective = newObjective
	if err := c.engine.store.UpsertBenchGoal(ctx, goal); err != nil {
		return err
	}
	note := strings.TrimSpace(userNote)
	if note == "" {
		note = newObjective
	}
	_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleUser, "", "CORRECTION: "+note)
	_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "",
		"Goal objective updated — do not redo completed assets; follow the correction.")
	if goal.RunID != "" {
		_ = c.engine.CancelRun(goal.RunID)
	}
	return nil
}

// ResumeBenchGoal continues a paused goal after Needs you is resolved.
// forceAgent, when non-empty, seats every subsequent unit onto that agent
// (same semantics as `qma run --agent`).
func (c *Conductor) ResumeBenchGoal(ctx context.Context, goalID, forceAgent string) error {
	if c.engine == nil || c.engine.store == nil {
		return fmt.Errorf("engine not configured")
	}
	goal, err := c.engine.store.GetBenchGoal(ctx, goalID)
	if err != nil {
		return err
	}
	switch goal.Status {
	case storage.GoalSucceeded, storage.GoalFailed, storage.GoalCancelled:
		return fmt.Errorf("goal %s is %s", goalID, goal.Status)
	}
	goal.Status = storage.GoalActive
	goal.Error = ""
	if err := c.engine.store.UpsertBenchGoal(ctx, goal); err != nil {
		return err
	}
	agent := strings.TrimSpace(forceAgent)
	if agent == "" {
		agent = c.forceAgent
	}
	note := "Resuming goal after Needs you was resolved."
	if agent != "" {
		note = fmt.Sprintf("Resuming goal with agent override %s.", agent)
	}
	_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "", note)

	projs, err := c.projects.List(ctx)
	if err != nil {
		return err
	}
	ref, ok := findProject(refsFromStorage(projs), goal.ProjectID)
	if !ok {
		return fmt.Errorf("project %s gone", goal.ProjectID)
	}
	c.startGoalLoop(goal.ID, ref, agent)
	return nil
}

// CancelBenchGoal stops an in-flight goal loop and marks the goal cancelled/failed.
func (c *Conductor) CancelBenchGoal(ctx context.Context, goalID, reason string) error {
	loops := c.goalLoops()
	loops.mu.Lock()
	if cancel, ok := loops.alive[goalID]; ok {
		cancel()
		delete(loops.alive, goalID)
	}
	loops.mu.Unlock()

	goal, err := c.engine.store.GetBenchGoal(ctx, goalID)
	if err != nil {
		return err
	}
	if goal.Status == storage.GoalSucceeded {
		return nil
	}
	goal.Status = storage.GoalCancelled
	if reason != "" {
		goal.Error = reason
	}
	_ = c.engine.store.UpsertBenchGoal(ctx, goal)
	_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "", "Goal cancelled: "+reason)
	return nil
}

// FormatGoalProgress returns a short goal status for chat (no thread spam).
// Use FormatGoalProgressVerbose for tool/debug dumps that include recent thread.
func (c *Conductor) FormatGoalProgress(ctx context.Context, projectID string) string {
	return c.formatGoalProgress(ctx, projectID, false)
}

// FormatGoalProgressVerbose includes the last few thread lines (for bench_thread tools).
func (c *Conductor) FormatGoalProgressVerbose(ctx context.Context, projectID string) string {
	return c.formatGoalProgress(ctx, projectID, true)
}

func (c *Conductor) formatGoalProgress(ctx context.Context, projectID string, verbose bool) string {
	if c.engine == nil || c.engine.store == nil {
		return ""
	}
	goal, err := c.engine.store.LatestBenchGoal(ctx, projectID)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Goal [%s] · iter %d · %s", goal.Status, goal.Iteration, truncateObj(goal.Objective, 100)))
	if goal.Error != "" {
		b.WriteString("\n")
		b.WriteString(truncateObj(goal.Error, 160))
	}
	if !verbose {
		// One latest non-system line if useful (e.g. DONE / attention).
		if msgs, err := c.engine.store.ListThreadMessages(ctx, goal.ThreadID, 4); err == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				m := msgs[i]
				if m.Role == storage.ThreadRoleSystem {
					continue
				}
				b.WriteString("\n")
				b.WriteString(truncateObj(m.Content, 120))
				break
			}
		}
		return b.String()
	}
	if goal.RunID != "" {
		b.WriteString(fmt.Sprintf("\nLatest unit: %s", goal.RunID))
	}
	msgs, err := c.engine.store.ListThreadMessages(ctx, goal.ThreadID, 8)
	if err == nil && len(msgs) > 0 {
		b.WriteString("\nRecent:")
		for _, m := range msgs {
			who := m.Role
			if m.AgentID != "" {
				who += ":" + m.AgentID
			}
			b.WriteString(fmt.Sprintf("\n- [%s] %s", who, truncateObj(m.Content, 120)))
		}
	}
	return b.String()
}

func (c *Conductor) startGoalLoop(goalID string, ref ProjectRef, forceAgent string) {
	loops := c.goalLoops()
	loops.mu.Lock()
	if cancel, ok := loops.alive[goalID]; ok {
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), benchGoalWallClock)
	loops.alive[goalID] = cancel
	loops.mu.Unlock()

	go func() {
		defer func() {
			loops.mu.Lock()
			delete(loops.alive, goalID)
			loops.mu.Unlock()
			cancel()
		}()
		c.runBenchGoalLoop(ctx, goalID, ref, forceAgent)
	}()
}

func (c *Conductor) runBenchGoalLoop(ctx context.Context, goalID string, ref ProjectRef, forceAgent string) {
	const softEscalateAfter = 2
	var softFails int
	var softSig string

	for {
		if ctx.Err() != nil {
			goal, _ := c.engine.store.GetBenchGoal(context.Background(), goalID)
			if goal.Status == storage.GoalActive {
				goal.Status = storage.GoalFailed
				goal.Error = "goal wall clock / cancelled"
				_ = c.engine.store.UpsertBenchGoal(context.Background(), goal)
				_ = c.appendThread(context.Background(), goal.ThreadID, storage.ThreadRoleSystem, "", goal.Error)
			}
			return
		}

		goal, err := c.engine.store.GetBenchGoal(ctx, goalID)
		if err != nil {
			return
		}
		if goal.Status != storage.GoalActive {
			return
		}
		if goal.Iteration >= benchGoalMaxIters {
			goal.Status = storage.GoalFailed
			goal.Error = fmt.Sprintf("hit iteration cap (%d) without leader declaring done", benchGoalMaxIters)
			_ = c.engine.store.UpsertBenchGoal(ctx, goal)
			_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "", goal.Error)
			return
		}

		decision, leaderID, derr := c.leaderNextUnit(ctx, goal, ref)
		if derr != nil {
			_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "", "Leader decide failed: "+derr.Error())
			if escalate, reason := shouldEscalateSoftFail(derr.Error(), &softFails, &softSig, softEscalateAfter); escalate {
				c.pauseGoalNeedsYou(ctx, goal, reason)
				return
			}
			// Soft: keep going with the original objective as next unit once.
			decision = leaderUnitDecision{NextObjective: goal.Objective, Summary: "Leader unavailable; retrying objective."}
		} else {
			_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleLeader, leaderID,
				fmt.Sprintf("decision done=%v next=%q summary=%s", decision.Done, decision.NextObjective, truncateObj(decision.Summary, 400)))
		}

		if decision.NeedsYou {
			c.pauseGoalNeedsYou(ctx, goal, decision.NeedsYouReason)
			return
		}
		if decision.Done {
			goal.Status = storage.GoalSucceeded
			if decision.Summary != "" {
				goal.Error = ""
				_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleLeader, leaderID, "DONE: "+decision.Summary)
			} else {
				_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleLeader, leaderID, "DONE")
			}
			_ = c.engine.store.UpsertBenchGoal(ctx, goal)
			return
		}

		next := strings.TrimSpace(decision.NextObjective)
		if next == "" {
			next = goal.Objective
		}

		goal.Iteration++
		_ = c.engine.store.UpsertBenchGoal(ctx, goal)
		_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "",
			fmt.Sprintf("Starting unit iteration %d: %s", goal.Iteration, next))

		runID, err := c.engine.StartUnitRun(ctx, PlanRequest{
			Objective: next, ProjectID: ref.ID, WorkDir: ref.Path, AgentOverride: forceAgent,
		})
		if err != nil {
			_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "", "Unit start failed: "+err.Error())
			if escalate, reason := shouldEscalateSoftFail(err.Error(), &softFails, &softSig, softEscalateAfter); escalate {
				c.pauseGoalNeedsYou(ctx, goal, reason)
				return
			}
			continue
		}
		goal.RunID = runID
		_ = c.engine.store.UpsertBenchGoal(ctx, goal)

		run, werr := c.engine.WaitRun(ctx, runID)
		summary := c.summarizeUnitRun(ctx, run)
		_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleWorker, "", summary)

		switch run.Status {
		case RunNeedsAttention:
			c.pauseGoalNeedsYou(ctx, goal, "Unit run needs your attention: "+run.Error)
			return
		case RunCancelled:
			if ctx.Err() != nil {
				return
			}
			softFails = 0
			softSig = ""
			_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "", "Unit cancelled; continuing goal.")
		case RunFailed:
			errText := run.Error
			if werr != nil {
				_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "", "Wait error: "+werr.Error())
				errText = werr.Error() + " " + errText
			}
			errText = errText + " " + summary
			if hard, msg := looksLikeHardNeedsYou(errText, 1); hard {
				c.pauseGoalNeedsYou(ctx, goal, msg+": "+truncateObj(errText, 240))
				return
			}
			if escalate, reason := shouldEscalateSoftFail(errText, &softFails, &softSig, softEscalateAfter); escalate {
				c.pauseGoalNeedsYou(ctx, goal, reason)
				return
			}
			_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "",
				"Unit failed (soft) — looping for recovery/replan.")
		case RunSucceeded:
			softFails = 0
			softSig = ""
			_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleSystem, "", "Unit succeeded.")
		}
	}
}

// shouldEscalateSoftFail bumps a consecutive-same-signature counter for auth /
// seating failures that otherwise spin forever. Returns Needs-you reason when
// the threshold is hit.
func shouldEscalateSoftFail(errText string, fails *int, sig *string, after int) (bool, string) {
	low := strings.ToLower(errText)
	kind := ""
	switch {
	case strings.Contains(low, "authentication required"),
		strings.Contains(low, "authorization required"),
		strings.Contains(low, "agent login"),
		strings.Contains(low, "please login"),
		strings.Contains(low, "please sign in"),
		strings.Contains(low, "please run 'agent login'"),
		strings.Contains(low, "please run \"agent login\""):
		kind = "auth"
	case strings.Contains(low, "no assigned agent"),
		strings.Contains(low, "has no assigned agent"):
		kind = "no_agent"
	case strings.Contains(low, "leader exit"),
		strings.Contains(low, "leader decide failed"),
		strings.Contains(low, "all leaders failed"),
		strings.Contains(low, "no leader available"):
		kind = "leader"
	default:
		*fails = 0
		*sig = ""
		return false, ""
	}
	if *sig != kind {
		*sig = kind
		*fails = 1
	} else {
		*fails++
	}
	if *fails < after {
		return false, ""
	}
	switch kind {
	case "auth":
		return true, "Agent authentication required (login) — paused after repeated failures. Run agent login, then resume."
	case "no_agent":
		return true, "Planner left tasks with no assigned agent — paused after repeated failures. Resume with an agent override (e.g. cursor)."
	default:
		return true, "Leader keep failing — paused after repeated failures. Check agent login / seating, then resume."
	}
}

func (c *Conductor) pauseGoalNeedsYou(ctx context.Context, goal storage.BenchGoal, reason string) {
	if reason == "" {
		reason = "Worker needs your attention"
	}
	goal.Status = storage.GoalPausedNeedsYou
	goal.Error = reason
	_ = c.engine.store.UpsertBenchGoal(ctx, goal)
	_ = c.appendThread(ctx, goal.ThreadID, storage.ThreadRoleAttention, "", reason)

	attnID := fmt.Sprintf("attn-goal-%s-%d", goal.ID, time.Now().UnixNano())
	_ = c.engine.RaiseAttention(ctx, storage.Attention{
		ID: attnID, ProjectID: goal.ProjectID, BenchID: goal.BenchID,
		RunID: goal.RunID, GoalID: goal.ID,
		Kind: "user_input", Message: reason + " (goal " + goal.ID + ")",
		Status: "open",
	})
}

func (c *Conductor) leaderNextUnit(ctx context.Context, goal storage.BenchGoal, ref ProjectRef) (leaderUnitDecision, string, error) {
	msgs, _ := c.engine.store.ListThreadMessages(ctx, goal.ThreadID, benchGoalMsgLimit)
	var thread strings.Builder
	for _, m := range msgs {
		thread.WriteString(fmt.Sprintf("%s: %s\n", m.Role, truncateObj(m.Content, 500)))
	}
	briefing := c.gatherWorkBriefing(ctx, []ProjectRef{ref}, ref.ID)

	prompt := fmt.Sprintf(`You are the project leader for %s driving a durable GOAL until it is done.
Silence never means yes — only set needs_you for real auth/login/merge/human decisions.

GOAL:
%s

LIVE BRIEFING:
%s

THREAD (recent):
%s

Respond with ONLY JSON (no markdown):
{
  "done": false,
  "summary": "short status",
  "next_objective": "one concrete unit of work for a worker (or empty if done)",
  "needs_you": false,
  "needs_you_reason": ""
}

Rules:
- Honor CORRECTION messages in the thread: they rewrite the goal. Never regenerate assets the user said to skip or that are already done.
- GPU / TRELLIS / UniRig: at most ONE heavy job at a time; wait for finish before the next (OOM otherwise).
- If the goal is complete (assets landed / verified per thread), set done=true and summarize.
- Otherwise set next_objective to the single highest-priority remaining unit (one asset or one verify step).
- Prefer small units. Do not ask to merge protected branches.
- needs_you=true only for auth/login or an unavoidable human choice.`,
		ref.Name, goal.Objective, truncateObj(briefing, 5000), thread.String())

	candidates := c.leaderCandidates(ctx, ref.ID)
	if len(candidates) == 0 {
		return leaderUnitDecision{}, "", fmt.Errorf("no leader available")
	}
	var lastErr error
	for _, leaderID := range candidates {
		adapter := c.adapterForID(ctx, leaderID)
		if adapter == nil {
			continue
		}
		exec, err := adapter.Execute(ctx, agents.AgentTask{
			ID:          fmt.Sprintf("goal-lead-%d", time.Now().UnixNano()),
			Description: "Bench goal next unit",
			WorkDir:     ref.Path,
			Prompt:      prompt,
			Timeout:     3 * time.Minute,
		})
		out := exec.Output
		combined := out
		if err != nil {
			combined = err.Error() + "\n" + out
		}
		if quota, reason := looksLikeQuotaExhausted(combined); quota || (err != nil && looksLikeQuotaErr(err)) {
			if !quota {
				reason = err.Error()
			}
			c.engine.MarkAgentExhausted(leaderID, reason)
			_, _ = c.engine.ReseatLeaderForProject(ctx, ref.ID, reason)
			lastErr = fmt.Errorf("%s: %s", leaderID, reason)
			continue
		}
		if err != nil || exec.ExitCode != 0 {
			lastErr = err
			if err == nil {
				lastErr = fmt.Errorf("leader exit %d", exec.ExitCode)
			}
			continue
		}
		dec, perr := parseLeaderUnitDecision(out)
		if perr != nil {
			lastErr = perr
			continue
		}
		return dec, leaderID, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all leaders failed")
	}
	return leaderUnitDecision{}, "", lastErr
}

func parseLeaderUnitDecision(raw string) (leaderUnitDecision, error) {
	raw = stripAgentCliNoise(raw)
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var d leaderUnitDecision
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return leaderUnitDecision{}, err
	}
	return d, nil
}

func (c *Conductor) ensureBenchThread(ctx context.Context, bench storage.Bench, projectID string) (storage.BenchThread, error) {
	if t, err := c.engine.store.GetBenchThreadByBench(ctx, bench.ID); err == nil {
		return t, nil
	}
	t := storage.BenchThread{
		ID:        "thread-" + bench.ID,
		BenchID:   bench.ID,
		ProjectID: projectID,
		CreatedAt: time.Now().UTC(),
	}
	if err := c.engine.store.UpsertBenchThread(ctx, t); err != nil {
		return storage.BenchThread{}, err
	}
	return t, nil
}

func (c *Conductor) appendThread(ctx context.Context, threadID, role, agentID, content string) error {
	if c.engine == nil || c.engine.store == nil || threadID == "" || strings.TrimSpace(content) == "" {
		return nil
	}
	return c.engine.store.AppendThreadMessage(ctx, storage.ThreadMessage{
		ID:       fmt.Sprintf("tm-%d", time.Now().UnixNano()),
		ThreadID: threadID,
		Role:     role,
		AgentID:  agentID,
		Content:  content,
	})
}

func (c *Conductor) summarizeUnitRun(ctx context.Context, run storage.Run) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("run %s [%s] %s", run.ID, run.Status, truncateObj(run.Objective, 80)))
	if run.Error != "" {
		b.WriteString(" err=" + truncateObj(run.Error, 120))
	}
	if c.engine != nil && c.engine.store != nil {
		tasks, _ := c.engine.store.ListTasksByRun(ctx, run.ID)
		for _, t := range tasks {
			b.WriteString(fmt.Sprintf("\n  - %s [%s] agent=%s", t.ID, t.Status, orDash(t.AssignedAgent)))
			if t.Error != "" {
				b.WriteString(" " + truncateObj(t.Error, 80))
			}
		}
	}
	return b.String()
}
