package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/events"
	"github.com/fernandotonon/QtMesh-Agents/internal/review"
	"github.com/fernandotonon/QtMesh-Agents/internal/scheduler"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
	"github.com/fernandotonon/QtMesh-Agents/internal/worktree"
)

// runIntegrate collects worktree diffs, runs static review, and raises a single
// merge_approval attention. It does not ask an LLM whether to apply.
func (e *Executor) runIntegrate(ctx context.Context, runID string, req PlanRequest, task *scheduler.Task) error {
	reply := strings.ToLower(humanReplyFromResult(task.ResultJSON))
	if isMergeApproveReply(reply) {
		return e.applyMergeToMain(ctx, runID, req, task)
	}
	if isMergeRejectReply(reply) {
		raw, _ := json.Marshal(map[string]any{
			"agent":   "integrate",
			"output":  "Human rejected apply-to-main; worktrees left in place.",
			"rejected": true,
		})
		task.ResultJSON = string(raw)
		if e.Emit != nil {
			_ = e.Emit(ctx, events.KindTaskCompleted, runID, task.ID, "integrate", "merge rejected; worktrees kept")
		}
		return nil
	}

	diff, paths, err := e.collectRunDiff(ctx, runID, req)
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		raw, _ := json.Marshal(map[string]any{
			"agent":  "integrate",
			"output": "No worktree diffs to integrate.",
		})
		task.ResultJSON = string(raw)
		if e.Emit != nil {
			_ = e.Emit(ctx, events.KindTaskCompleted, runID, task.ID, "integrate", "nothing to apply")
		}
		return nil
	}
	rep := review.ReviewDiff(task.ID, diff)
	_ = e.persistReview(ctx, runID, task, rep, diff)

	summary := fmt.Sprintf("Integrate review: %s\nFiles touched across worktrees: %d\n%s",
		rep.Summary, len(paths), strings.Join(paths, "\n"))
	if len(rep.Findings) > 0 {
		var lines []string
		for _, f := range rep.Findings {
			p := f.Path
			if p == "" {
				p = "-"
			}
			lines = append(lines, fmt.Sprintf("[%s] %s: %s", f.Severity, p, f.Message))
		}
		summary += "\nFindings:\n" + strings.Join(lines, "\n")
	}
	raw, _ := json.Marshal(map[string]any{
		"agent":    "integrate",
		"output":   summary,
		"approved": rep.Approved,
		"paths":    paths,
	})
	task.ResultJSON = string(raw)

	if !rep.Approved {
		msg := "Static review blocked integrate: " + rep.Summary
		return e.pauseForAttentionKind(ctx, runID, task, "merge_approval", msg+"\n"+summary)
	}

	msg := "Ready to apply worktree patches to the main working tree (not a git merge to main/master).\n" +
		"Review findings: " + rep.Summary + "\n" +
		"OPTIONS:\n" +
		"1) Approve apply to main working tree\n" +
		"2) Reject leave worktree\n" +
		"3) Show findings"
	return e.pauseForAttentionKind(ctx, runID, task, "merge_approval", msg)
}

func (e *Executor) collectRunDiff(ctx context.Context, runID string, req PlanRequest) (string, []string, error) {
	if e.Store == nil {
		return "", nil, fmt.Errorf("store not configured")
	}
	recs, err := e.Store.ListTasksByRun(ctx, runID)
	if err != nil {
		return "", nil, err
	}
	var parts []string
	var paths []string
	seen := map[string]bool{}
	mgr := worktree.New(req.WorkDir, e.WorktreeBase)
	for _, rec := range recs {
		if isIntegrateTask(&scheduler.Task{ID: rec.ID, Category: rec.Category}) ||
			rec.Category == "leadership" || rec.Category == "architecture" {
			continue
		}
		diff := ""
		if rec.DiffPath != "" {
			b, rerr := os.ReadFile(rec.DiffPath)
			if rerr == nil {
				diff = string(b)
			}
		}
		if diff == "" && rec.WorktreePath != "" {
			d, derr := mgr.Diff(ctx, rec.WorktreePath)
			if derr == nil {
				diff = d
			}
		}
		if strings.TrimSpace(diff) == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("# task %s\n%s", rec.ID, diff))
		for _, p := range diffPaths(diff) {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	return strings.Join(parts, "\n\n"), paths, nil
}

func diffPaths(diff string) []string {
	var out []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ ") {
			p := strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			if p != "" && p != "/dev/null" {
				out = append(out, p)
			}
		}
	}
	return out
}

func (e *Executor) applyMergeToMain(ctx context.Context, runID string, req PlanRequest, task *scheduler.Task) error {
	if e.Store == nil {
		return fmt.Errorf("store not configured")
	}
	recs, err := e.Store.ListTasksByRun(ctx, runID)
	if err != nil {
		return err
	}
	var applied []string
	var failed []string
	for _, rec := range recs {
		if rec.WorktreePath == "" && rec.DiffPath == "" {
			continue
		}
		if isIntegrateTask(&scheduler.Task{ID: rec.ID, Category: rec.Category}) {
			continue
		}
		patch := ""
		if rec.DiffPath != "" {
			b, rerr := os.ReadFile(rec.DiffPath)
			if rerr == nil {
				patch = string(b)
			}
		}
		if strings.TrimSpace(patch) == "" && rec.WorktreePath != "" {
			d, derr := worktree.New(req.WorkDir, e.WorktreeBase).Diff(ctx, rec.WorktreePath)
			if derr == nil {
				patch = d
			}
		}
		if strings.TrimSpace(patch) == "" {
			continue
		}
		if aerr := applyUnifiedDiff(req.WorkDir, patch); aerr != nil {
			if rec.WorktreePath != "" {
				if cerr := copyWorktreeFiles(req.WorkDir, rec.WorktreePath, diffPaths(patch)); cerr == nil {
					applied = append(applied, rec.ID)
					continue
				}
			}
			failed = append(failed, fmt.Sprintf("%s: %v", rec.ID, aerr))
			continue
		}
		applied = append(applied, rec.ID)
	}
	out := fmt.Sprintf("Applied patches from tasks: %s", strings.Join(applied, ", "))
	if len(failed) > 0 {
		out += "\nFailed: " + strings.Join(failed, "; ")
		raw, _ := json.Marshal(map[string]any{"agent": "integrate", "output": out, "applied": applied, "failed": failed})
		task.ResultJSON = string(raw)
		return fmt.Errorf("apply to main incomplete: %s", strings.Join(failed, "; "))
	}
	if len(applied) == 0 {
		out = "No worktree diffs to apply (already clean or empty)."
	}
	raw, _ := json.Marshal(map[string]any{"agent": "integrate", "output": out, "applied": applied})
	task.ResultJSON = string(raw)
	if e.Emit != nil {
		_ = e.Emit(ctx, events.KindTaskCompleted, runID, task.ID, "integrate", out)
	}
	return nil
}

func applyUnifiedDiff(workDir, patch string) error {
	tmp, err := os.CreateTemp("", "qma-patch-*.diff")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(patch); err != nil {
		_ = tmp.Close()
		return err
	}
	_ = tmp.Close()
	cmd := exec.Command("git", "-C", workDir, "apply", "--3way", "--whitespace=nowarn", tmp.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback: checkout changed files from the first worktree path encoded in patch headers is hard;
		// try plain apply without 3way.
		cmd2 := exec.Command("git", "-C", workDir, "apply", "--whitespace=nowarn", tmp.Name())
		out2, err2 := cmd2.CombinedOutput()
		if err2 != nil {
			return fmt.Errorf("%w: %s; fallback: %v: %s", err, strings.TrimSpace(string(out)), err2, strings.TrimSpace(string(out2)))
		}
	}
	return nil
}

func isMergeApproveReply(reply string) bool {
	low := strings.ToLower(reply)
	needles := []string{
		"approve apply",
		"approve apply to main",
		"apply to main working tree",
		"approve merge",
		"yes, apply",
		"approved — apply",
		"approved - apply",
	}
	for _, n := range needles {
		if strings.Contains(low, n) {
			return true
		}
	}
	// Exact-ish affirmative for merge_approval options.
	if strings.Contains(low, "approve") && strings.Contains(low, "main") {
		return true
	}
	return false
}

func isMergeRejectReply(reply string) bool {
	low := strings.ToLower(reply)
	if strings.Contains(low, "show findings") {
		return false
	}
	return strings.Contains(low, "reject") || strings.Contains(low, "leave worktree") ||
		strings.Contains(low, "do not apply")
}

func (e *Executor) reviewTaskDiff(ctx context.Context, runID string, task *scheduler.Task) error {
	b, err := os.ReadFile(task.DiffPath)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return nil // no diff file — skip
	}
	rep := review.ReviewDiff(task.ID, string(b))
	_ = e.persistReview(ctx, runID, task, rep, string(b))
	if !rep.Approved {
		msg := "Static review blocked task: " + rep.Summary
		for _, f := range rep.Findings {
			if f.Severity == review.SeverityError {
				msg += fmt.Sprintf("\n- %s: %s", f.Path, f.Message)
			}
		}
		return e.pauseForAttentionKind(ctx, runID, task, "review_block", msg)
	}
	return nil
}

func (e *Executor) persistReview(ctx context.Context, runID string, task *scheduler.Task, rep review.Report, diff string) error {
	if e.Store == nil {
		return nil
	}
	findings, _ := json.Marshal(rep.Findings)
	rec := storage.ReviewRecord{
		ID:        fmt.Sprintf("rev-%s-%s-%d", runID, task.ID, time.Now().UnixNano()),
		RunID:     runID,
		TaskID:    task.ID,
		ProjectID: e.ProjectID,
		Summary:   rep.Summary,
		Approved:  rep.Approved,
		Findings:  string(findings),
		DiffSnippet: trimOut(diff),
	}
	return e.Store.UpsertReview(ctx, rec)
}

func (e *Executor) pauseForAttentionKind(ctx context.Context, runID string, task *scheduler.Task, kind, msg string) error {
	already := task.Status == scheduler.StatusAwaitingInput
	task.Status = scheduler.StatusAwaitingInput
	task.Error = msg
	_ = e.persist(ctx, runID, task)
	if !already && e.RaiseAttention != nil {
		if !e.shouldRaiseFingerprint(runID, task.ID, msg) {
			return fmt.Errorf("%w: %s", scheduler.ErrNeedsAttention, msg)
		}
		_ = e.RaiseAttention(ctx, storage.Attention{
			ProjectID: e.ProjectID,
			BenchID:   e.BenchID,
			RunID:     runID,
			TaskID:    task.ID,
			AgentID:   task.AssignedAgent,
			Kind:      kind,
			Message:   fmt.Sprintf("%s (task %s / agent %s)", msg, shortID(task.ID), orAgent(task.AssignedAgent, "integrate")),
			Status:    "open",
		})
	}
	return fmt.Errorf("%w: %s", scheduler.ErrNeedsAttention, msg)
}

func orAgent(id, fallback string) string {
	if id == "" {
		return fallback
	}
	return id
}

// Copy changed files from a worktree into the main project path (used when git apply fails).
func copyWorktreeFiles(mainDir, wtDir string, relPaths []string) error {
	for _, rel := range relPaths {
		src := filepath.Join(wtDir, rel)
		dst := filepath.Join(mainDir, rel)
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
