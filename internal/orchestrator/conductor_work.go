package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
	"github.com/fernandotonon/QtMesh-Agents/pkg/version"
)

// gatherWorkBriefing builds an authoritative snapshot of project git state,
// active runs/tasks, and open attention for the orchestrator to answer from.
// focusProjectID scopes to one project when non-empty.
func (c *Conductor) gatherWorkBriefing(ctx context.Context, refs []ProjectRef, focusProjectID string) string {
	var b strings.Builder
	b.WriteString("LIVE WORK BRIEFING (authoritative — inspect this before answering)\n")

	focusRefs := refs
	if focusProjectID != "" {
		if ref, ok := findProject(refs, focusProjectID); ok {
			focusRefs = []ProjectRef{ref}
			b.WriteString(fmt.Sprintf("Focus project: %s (%s)\n", ref.Name, ref.ID))
		}
	}
	if len(focusRefs) == 0 {
		b.WriteString("No registered projects.\n")
		return b.String()
	}

	b.WriteString("\n## Installed qma\n")
	b.WriteString(localBinaryBrief())

	for _, ref := range focusRefs {
		b.WriteString(fmt.Sprintf("\n## Project %s (%s)\n", ref.Name, ref.ID))
		b.WriteString(fmt.Sprintf("Path: %s\n", ref.Path))
		b.WriteString(gitSnapshot(ref.Path))
		b.WriteString(featureBranchesBrief(ref.Path))

		if c.engine == nil || c.engine.store == nil {
			continue
		}
		if bench, err := c.engine.store.GetBenchByProject(ctx, ref.ID); err == nil {
			b.WriteString(fmt.Sprintf("Bench: %s leader=%s status=%s\n", bench.ID, bench.LeaderAgentID, bench.Status))
		}
		runs, err := c.engine.store.ListRuns(ctx, 20)
		if err != nil {
			continue
		}
		var projRuns []storage.Run
		for _, r := range runs {
			if r.ProjectID != ref.ID {
				continue
			}
			projRuns = append(projRuns, r)
			if len(projRuns) >= 5 {
				break
			}
		}
		if len(projRuns) == 0 {
			b.WriteString("Runs: none recent\n")
			continue
		}
		b.WriteString("Runs:\n")
		var landings []string
		for _, r := range projRuns {
			b.WriteString(fmt.Sprintf("  • %s [%s] %s\n", r.ID, r.Status, truncateObj(r.Objective, 72)))
			if r.Error != "" {
				b.WriteString("      run error: " + truncateObj(r.Error, 100) + "\n")
			}
			tasks, err := c.engine.store.ListTasksByRun(ctx, r.ID)
			if err != nil {
				continue
			}
			for _, t := range tasks {
				line := fmt.Sprintf("      - %s [%s] agent=%s", shortTaskTail(t.ID), t.Status, orDash(t.AssignedAgent))
				if t.Branch != "" {
					line += " branch=" + t.Branch
				}
				if t.Error != "" {
					line += " err=" + truncateObj(t.Error, 60)
				}
				b.WriteString(line + "\n")
				if t.WorktreePath != "" {
					b.WriteString("        worktree: " + t.WorktreePath + "\n")
					landings = append(landings, formatLanding(t))
					// Always show worker worktree git — this is where code usually is.
					snap := gitSnapshotCompact(t.WorktreePath)
					for _, ln := range strings.Split(strings.TrimSpace(snap), "\n") {
						if ln == "" {
							continue
						}
						b.WriteString("        " + ln + "\n")
					}
				}
				if t.DiffPath != "" {
					b.WriteString("        diff file: " + t.DiffPath + "\n")
				}
				if excerpt := taskResultExcerpt(t, 200); excerpt != "" &&
					(t.Status == "succeeded" || t.Status == "awaiting_input" || t.Status == "failed") {
					b.WriteString("        result: " + excerpt + "\n")
				}
			}
		}
		b.WriteString("\n### Where worker code landed\n")
		if len(landings) == 0 {
			b.WriteString(scanQmaWorktrees(ref.Path))
		} else {
			for _, L := range landings {
				b.WriteString(L)
			}
			b.WriteString(scanQmaWorktrees(ref.Path))
		}
		b.WriteString("Note: workers commit in isolated worktrees/branches. Main tree stays unchanged until an integrate task succeeds (and merge still needs your approval).\n")
	}

	blocked := c.gatherBlockedAgents(ctx)
	if len(blocked) > 0 {
		b.WriteString("\n## Needs you\n")
		b.WriteString(formatLiveSituation(blocked))
	} else {
		b.WriteString("\n## Needs you\nNone open.\n")
	}

	if c.engine != nil && c.engine.store != nil {
		if mets, err := c.engine.store.ListRunMetrics(ctx, 5); err == nil && len(mets) > 0 {
			b.WriteString("\n## Recent metrics\n")
			for _, m := range mets {
				mark := "fail"
				if m.Success {
					mark = "ok"
				}
				b.WriteString(fmt.Sprintf("  • %s %s %dms retries=%d agent=%s\n",
					shortTaskTail(m.RunID), mark, m.DurationMS, m.Retries, orDash(m.AgentID)))
			}
		}
	}
	return b.String()
}

func (c *Conductor) latestRunVerdict(ctx context.Context, refs []ProjectRef, focusProjectID string) string {
	var b strings.Builder
	b.WriteString("## Verdict\n")
	b.WriteString(localBinaryBrief())
	if c.engine == nil || c.engine.store == nil {
		b.WriteString("No engine — cannot check runs.\n")
		return b.String()
	}
	runs, err := c.engine.store.ListRuns(ctx, 10)
	if err != nil || len(runs) == 0 {
		b.WriteString("No runs recorded yet.\n")
		return b.String()
	}
	var latest *storage.Run
	for i := range runs {
		r := &runs[i]
		if focusProjectID != "" && r.ProjectID != focusProjectID {
			continue
		}
		if focusProjectID == "" && len(refs) > 0 {
			// prefer focused or first matching registered project
			ok := false
			for _, ref := range refs {
				if r.ProjectID == ref.ID {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		latest = r
		break
	}
	if latest == nil {
		latest = &runs[0]
	}
	b.WriteString(fmt.Sprintf("Latest run: `%s` → **%s**\n", latest.ID, latest.Status))
	b.WriteString("Objective: " + truncateObj(latest.Objective, 120) + "\n")
	if latest.Error != "" {
		b.WriteString("Error: " + latest.Error + "\n")
	}
	tasks, _ := c.engine.store.ListTasksByRun(ctx, latest.ID)
	var blocked, failed, okN int
	for _, t := range tasks {
		switch t.Status {
		case "succeeded":
			okN++
		case "failed":
			failed++
		case "blocked", "awaiting_input":
			blocked++
		}
		if t.WorktreePath != "" {
			b.WriteString(fmt.Sprintf("- task %s worktree: %s (branch %s)\n", shortTaskTail(t.ID), t.WorktreePath, orDash(t.Branch)))
		}
	}
	b.WriteString(fmt.Sprintf("Tasks: %d succeeded, %d failed, %d blocked/waiting\n", okN, failed, blocked))
	switch latest.Status {
	case RunSucceeded:
		b.WriteString("Answer: **yes** — the latest run succeeded. Check \"Where worker code landed\" below; main tree may still need integrate/merge.\n")
	case RunRunning, RunPlanning, RunNeedsAttention:
		b.WriteString("Answer: **not yet** — the latest run is still in progress or needs you.\n")
	default:
		b.WriteString("Answer: **no** — the latest run did not complete successfully. Worker changes (if any) are in worktrees/feature branches, not necessarily your main checkout or the installed binary.\n")
	}
	return b.String()
}

func localBinaryBrief() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Build version string: %s\n", version.Version))
	if p, err := exec.LookPath("qma"); err == nil {
		b.WriteString("PATH qma: " + p + "\n")
		if fi, err := os.Stat(p); err == nil {
			b.WriteString(fmt.Sprintf("  mtime: %s  size: %d\n", fi.ModTime().UTC().Format(time.RFC3339), fi.Size()))
		}
		if target, err := filepath.EvalSymlinks(p); err == nil && target != p {
			b.WriteString("  resolves to: " + target + "\n")
			if fi, err := os.Stat(target); err == nil {
				b.WriteString(fmt.Sprintf("  target mtime: %s\n", fi.ModTime().UTC().Format(time.RFC3339)))
			}
		}
	} else {
		b.WriteString("PATH qma: not found\n")
	}
	return b.String()
}

func featureBranchesBrief(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "branch", "--list", "feature/*", "--list", "qma/*")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil || text == "" {
		return "Feature branches: (none matching feature/* or qma/*)\n"
	}
	var b strings.Builder
	b.WriteString("Feature branches:\n")
	for i, line := range strings.Split(text, "\n") {
		if i >= 12 {
			b.WriteString("  …\n")
			break
		}
		b.WriteString("  " + strings.TrimSpace(line) + "\n")
	}
	return b.String()
}

func formatLanding(t storage.TaskRecord) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("- task %s [%s] agent=%s\n", shortTaskTail(t.ID), t.Status, orDash(t.AssignedAgent)))
	if t.Branch != "" {
		b.WriteString("    branch: " + t.Branch + "\n")
	}
	if t.WorktreePath != "" {
		b.WriteString("    path: " + t.WorktreePath + "\n")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "git", "-C", t.WorktreePath, "log", "-3", "--oneline").CombinedOutput(); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line != "" {
					b.WriteString("    commit: " + line + "\n")
				}
			}
		}
	}
	return b.String()
}

func scanQmaWorktrees(projectPath string) string {
	root := filepath.Join(projectPath, ".qma-worktrees")
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) == 0 {
		return "(no .qma-worktrees on disk for this project)\n"
	}
	var b strings.Builder
	b.WriteString("On-disk .qma-worktrees:\n")
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n++
		if n > 8 {
			b.WriteString("  …\n")
			break
		}
		p := filepath.Join(root, e.Name())
		b.WriteString("  - " + p + "\n")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if out, err := exec.CommandContext(ctx, "git", "-C", p, "log", "-2", "--oneline", "--decorate").CombinedOutput(); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line != "" {
					b.WriteString("      " + line + "\n")
				}
			}
		}
		cancel()
	}
	if n == 0 {
		return "(no .qma-worktrees dirs)\n"
	}
	return b.String()
}

func gitSnapshotCompact(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	var b strings.Builder
	run := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	if br := run("branch", "--show-current"); br != "" {
		b.WriteString("branch: " + br + "\n")
	}
	if st := run("status", "--short"); st != "" {
		lines := strings.Split(st, "\n")
		if len(lines) > 8 {
			lines = append(lines[:8], "…")
		}
		b.WriteString("status: " + strings.Join(lines, " | ") + "\n")
	}
	if log := run("log", "-3", "--oneline"); log != "" {
		b.WriteString("log: " + strings.ReplaceAll(log, "\n", " · ") + "\n")
	}
	return b.String()
}

func shortTaskTail(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return id
}

func gitSnapshot(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return "Git: (no path)\n"
	}
	var b strings.Builder
	run := func(title string, args ...string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		text := strings.TrimSpace(string(out))
		if err != nil && text == "" {
			b.WriteString(fmt.Sprintf("%s: (unavailable)\n", title))
			return
		}
		if text == "" {
			b.WriteString(fmt.Sprintf("%s: (clean/empty)\n", title))
			return
		}
		lines := strings.Split(text, "\n")
		if len(lines) > 40 {
			lines = append(lines[:40], "…")
		}
		b.WriteString(title + ":\n")
		for _, line := range lines {
			b.WriteString("  " + line + "\n")
		}
	}
	run("git status", "status", "--short")
	run("git diff --stat", "diff", "--stat", "HEAD")
	run("git log", "log", "-5", "--oneline")
	return b.String()
}

func taskResultExcerpt(t storage.TaskRecord, max int) string {
	out := outputFromResultJSON(t.ResultJSON)
	out = strings.TrimSpace(stripEchoedUserPrompt(out))
	if out == "" {
		return ""
	}
	// Prefer trailing assistant-looking content.
	lines := strings.Split(out, "\n")
	var keep []string
	for i := len(lines) - 1; i >= 0 && len(keep) < 6; i-- {
		trim := strings.TrimSpace(lines[i])
		if trim == "" {
			continue
		}
		low := strings.ToLower(trim)
		if strings.HasPrefix(trim, "func ") || strings.HasPrefix(trim, "+") || strings.HasPrefix(trim, "-") ||
			strings.Contains(low, "mustcompile") || strings.HasPrefix(trim, "import ") {
			continue
		}
		keep = append([]string{trim}, keep...)
	}
	if len(keep) == 0 {
		return truncateObj(out, max)
	}
	return truncateObj(strings.Join(keep, " | "), max)
}

func isWorkSummaryQuery(lower string) bool {
	if isContinueIntent(lower) {
		return false
	}
	keys := []string{
		"what changed", "what improved", "what did you", "what's different",
		"whats different", "summarize work", "summary of work", "what have you done",
		"what have we done", "how's the project", "how is the project",
		"looks the same", "which improvements", "improvements implemented",
		"what was implemented", "diff summary", "show the diff",
		"where did", "where is the code", "which branch", "worktree",
	}
	for _, k := range keys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	if strings.Contains(lower, "where") && (strings.Contains(lower, "commit") || strings.Contains(lower, "code") || strings.Contains(lower, "branch")) {
		return true
	}
	return false
}

// isProgressQuery matches status/progress asks like "how is the deckbot generation going?".
func isProgressQuery(lower string) bool {
	// Fresh generate / continue-generating is handled as work, not status-only.
	if isFreshWorkIntent(lower) || isLeaderActionIntent(lower) {
		return false
	}
	if isResumeOnlyIntent(lower) {
		return false
	}
	if isGenerationStatusAsk(lower) {
		return true
	}
	if isGenerationStatusAsk(lower) {
		return true
	}
	keys := []string{
		"how is ", "how's ", "hows ", "how are ",
		"going on", "going with", "any update", "any updates",
		"progress on", "progress with", "what's the status", "whats the status",
		"status of", "status on", "generation going", "still working",
		"how's it going", "hows it going", "how is it going",
	}
	for _, k := range keys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	if lower == "status" || strings.HasPrefix(lower, "status ") || strings.HasSuffix(lower, " status?") {
		return true
	}
	return false
}

// isInterrogative is true for question-like turns that must never get canned "Happy to help" text.
func isInterrogative(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "?") {
		return true
	}
	return strings.HasPrefix(lower, "how ") || strings.HasPrefix(lower, "what ") ||
		strings.HasPrefix(lower, "where ") || strings.HasPrefix(lower, "why ") ||
		strings.HasPrefix(lower, "when ") || strings.HasPrefix(lower, "who ") ||
		strings.HasPrefix(lower, "which ") || strings.HasPrefix(lower, "did ") ||
		strings.HasPrefix(lower, "do ") || strings.HasPrefix(lower, "does ") ||
		strings.HasPrefix(lower, "is ") || strings.HasPrefix(lower, "are ") ||
		strings.HasPrefix(lower, "was ") || strings.HasPrefix(lower, "were ") ||
		strings.HasPrefix(lower, "can ") || strings.HasPrefix(lower, "could ") ||
		strings.HasPrefix(lower, "should ") || strings.HasPrefix(lower, "will ")
}

// resolveChatProjectID picks TUI focus, then a single MatchProjects hit, then the only registered project.
func resolveChatProjectID(userText, focus string, refs []ProjectRef) string {
	if pid := strings.TrimSpace(focus); pid != "" {
		if _, ok := findProject(refs, pid); ok {
			return pid
		}
	}
	if matched := MatchProjects(userText, refs); len(matched) == 1 {
		return matched[0].ID
	}
	if len(refs) == 1 {
		return refs[0].ID
	}
	return ""
}

func isOutcomeQuery(lower string) bool {
	keys := []string{
		"did it work", "did that work", "did the run work", "did the install",
		"is it installed", "is it running the latest", "latest version",
		"did it succeed", "did we succeed", "is it done", "are we done",
		"did it finish", "was it successful", "install work", "daemon running",
	}
	for _, k := range keys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	// short forms
	if lower == "did it work?" || lower == "did it work" || lower == "worked?" {
		return true
	}
	return false
}

func isLeaderConsultQuery(lower string) bool {
	return strings.Contains(lower, "ask the leader") ||
		strings.Contains(lower, "ask leader") ||
		strings.Contains(lower, "ask the lead") ||
		strings.Contains(lower, "what does the leader") ||
		strings.Contains(lower, "what does the lead think") ||
		strings.Contains(lower, "coordinate with the leader") ||
		strings.Contains(lower, "consult the leader") ||
		strings.Contains(lower, "talk to the leader")
}

func isJudgmentQuery(lower string) bool {
	if isContinueIntent(lower) || isWorkSummaryQuery(lower) {
		return false
	}
	keys := []string{
		"what should we do next", "what should i do next", "what's next",
		"whats next", "prioritize", "is this ready", "are we ready",
		"should we ship", "should we merge", "recommend",
	}
	for _, k := range keys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}
