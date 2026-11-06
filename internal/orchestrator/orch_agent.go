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

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

const orchAgentMaxIters = 8

type agentToolCall struct {
	Name string            `json:"name"`
	Args map[string]string `json:"args"`
}

type agentTurn struct {
	Tools   []agentToolCall `json:"tools"`
	Answer  string          `json:"answer"`
	Actions []ChatAction    `json:"actions"`
}

// agenticChat runs a tool-using orchestrator loop with the conversational adapter
// (Cursor preferred). On quota/token exhaustion it fails over cursor → claude → codex.
// Falls back to interpret/heuristic when no adapter is usable.
func (c *Conductor) agenticChat(ctx context.Context, userText string, refs []ProjectRef, focusProjectID string) (ChatResult, error) {
	adapters := c.orchChatAdapters(ctx)
	if len(adapters) == 0 {
		return ChatResult{}, fmt.Errorf("no orch agent adapter")
	}

	var trace []string
	briefing := c.gatherWorkBriefing(ctx, refs, focusProjectID)
	var last agentTurn
	adapterIdx := 0
	adapter := adapters[adapterIdx]

	for i := 0; i < orchAgentMaxIters; i++ {
		prompt := c.orchAgentPrompt(userText, refs, focusProjectID, briefing, trace, i)
		execRes, err := adapter.Execute(ctx, agents.AgentTask{
			ID:          fmt.Sprintf("orch-agent-%d-%d", time.Now().UnixNano(), i),
			Description: "QtMesh orchestrator agent turn",
			Prompt:      prompt,
			Timeout:     4 * time.Minute,
		})
		combined := execRes.Output
		if err != nil {
			combined = err.Error() + "\n" + execRes.Output
		}
		if quota, reason := looksLikeQuotaExhausted(combined); quota || (err != nil && looksLikeQuotaErr(err)) {
			if !quota {
				reason = err.Error()
			}
			if c.engine != nil {
				c.engine.MarkAgentExhausted(adapter.ID(), reason)
			}
			if adapterIdx+1 < len(adapters) {
				adapterIdx++
				adapter = adapters[adapterIdx]
				c.adapter = adapter // sticky for subsequent turns this process
				trace = append(trace, fmt.Sprintf("ORCH FAILOVER → %s (%s)", adapter.ID(), reason))
				i-- // retry same iteration with the next brain
				continue
			}
			if i == 0 {
				return ChatResult{}, fmt.Errorf("orch agent quota exhausted: %s", reason)
			}
			break
		}
		if err != nil || execRes.ExitCode != 0 {
			if i == 0 {
				return ChatResult{}, fmt.Errorf("orch agent execute failed: %v", err)
			}
			break
		}
		turn, perr := parseAgentTurn(execRes.Output)
		if perr != nil || (turn.Answer == "" && len(turn.Tools) == 0) {
			// Treat raw prose as the final answer.
			raw := strings.TrimSpace(stripAgentCliNoise(execRes.Output))
			if raw != "" {
				return ChatResult{Reply: raw}, nil
			}
			break
		}
		last = turn
		if len(turn.Tools) == 0 {
			break
		}
		for _, tc := range turn.Tools {
			result := c.execOrchTool(ctx, tc, refs, focusProjectID)
			trace = append(trace, fmt.Sprintf("TOOL %s\nARGS %v\nRESULT:\n%s", tc.Name, tc.Args, truncateObj(result, 6000)))
		}
	}

	result := ChatResult{Reply: strings.TrimSpace(last.Answer)}
	if result.Reply == "" {
		result.Reply = "I investigated with tools but could not form a final answer. Try asking more specifically, or say \"what changed?\"."
	}
	if len(last.Actions) > 0 {
		result = c.applyGoalActions(ctx, result.Reply, last.Actions, refs, false)
	}
	return result, nil
}

// orchChatAdapters returns Detect-able conversational brains in failover order
// (cursor → claude → codex), skipping agents marked exhausted.
func (c *Conductor) orchChatAdapters(ctx context.Context) []agents.AgentAdapter {
	order := OrchChatOrder()
	seen := map[string]bool{}
	var out []agents.AgentAdapter
	add := func(ad agents.AgentAdapter) {
		if ad == nil {
			return
		}
		id := ad.ID()
		if id == "" || seen[id] {
			return
		}
		if c.engine != nil && c.engine.IsAgentExhausted(id) {
			return
		}
		ok, err := ad.Detect(ctx)
		if err != nil || !ok {
			return
		}
		seen[id] = true
		out = append(out, ad)
	}
	// Prefer the currently sticky adapter first when still healthy.
	add(c.adapter)
	for _, id := range order {
		add(c.adapterForID(ctx, id))
	}
	return out
}

func (c *Conductor) orchAgentPrompt(userText string, refs []ProjectRef, focus, briefing string, trace []string, iter int) string {
	var hist strings.Builder
	h := c.History()
	start := 0
	if len(h) > 10 {
		start = len(h) - 10
	}
	for _, m := range h[start:] {
		if m.Role == "system" {
			continue
		}
		hist.WriteString(m.Role + ": " + m.Content + "\n")
	}
	var tr strings.Builder
	if len(trace) == 0 {
		tr.WriteString("(none yet — call tools before answering when you need facts)\n")
	} else {
		for _, t := range trace {
			tr.WriteString(t)
			tr.WriteString("\n---\n")
		}
	}
	focusNote := "none"
	if focus != "" {
		focusNote = focus
	}
	return fmt.Sprintf(`You are the QtMesh Agents ORCHESTRATOR — the leader of project leaders.
Each registered project has a bench with a seated leader (claude/cursor/codex) and workers.
You manage across benches: consult leaders for status, synthesize, and dispatch work through them.
You are NOT a passive chatbot and NOT a single-project coder. Investigate with tools, then answer clearly.

Adapter note: you may be Cursor, Claude, or another CLI — still only use the tool protocol below for QtMesh actions.

Registered projects:
%s
TUI focus project: %s

SEED BRIEFING (may be stale — re-check with tools when answering "did it work" / where code is):
%s

Conversation:
%s

Tool results so far (iteration %d):
%s

User message:
%s

Respond with ONLY one JSON object (no markdown fence required):
{
  "tools": [ {"name": "<tool>", "args": {"key":"value"}} ],
  "answer": "<final markdown reply to the user, or empty if you still need tools>",
  "actions": [ {"type":"run","project_id":"<id>","objective":"<text>"} ]
}

Available tools (name → args):
- work_briefing: optional project_id
- bench_status: optional project_id  (all open benches + latest run per project)
- git_status: project_id
- git_diff: project_id
- git_log: project_id
- find_worktrees: project_id
- list_runs: optional project_id
- run_detail: run_id
- list_attention: (none)
- binary_info: (none)
- install_qma: project_id  (builds/installs qma binary; does NOT restart daemon)
- ask_leader: project_id, question
- ask_leaders: question, optional project_ids (comma-separated; default = all matched / all registered)
- bench_thread: optional project_id  (recent work-thread messages + goal status)
- start_goal: project_id, objective  (durable bench goal loop; prefer actions[] type=run)
- start_run: project_id, objective  (alias of start_goal)

Rules:
- Status/progress ("how is X going", "generated?"): call bench_thread / bench_status; answer in 1–3 short lines — never start_goal.
- Prefer tools over guessing. Never invent commits, worktree paths, or run status.
- NEW work + no active goal → actions[] / start_goal. Active goal + correction ("do not generate X", "only the other 2") → do NOT start_goal; host amends the objective.
- Keep answers clean: no full thread dumps, no run-id/iteration spam, no pasted briefing unless asked.
- Do not merge to main. Do not push.
`, formatProjectList(refs), focusNote, truncateObj(briefing, 8000), hist.String(), iter+1, tr.String(), userText)
}

func parseAgentTurn(raw string) (agentTurn, error) {
	raw = stripAgentCliNoise(raw)
	raw = strings.TrimSpace(raw)
	// Extract JSON object.
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var turn agentTurn
	if err := json.Unmarshal([]byte(raw), &turn); err != nil {
		return agentTurn{}, err
	}
	return turn, nil
}

func stripAgentCliNoise(s string) string {
	lower := strings.ToLower(s)
	for _, marker := range []string{"\nassistant\n", "\nfinal answer\n", "\nfinal:\n"} {
		if i := strings.LastIndex(lower, marker); i >= 0 {
			s = s[i+len(marker):]
			lower = strings.ToLower(s)
		}
	}
	return strings.TrimSpace(s)
}

func (c *Conductor) execOrchTool(ctx context.Context, tc agentToolCall, refs []ProjectRef, focus string) string {
	name := strings.ToLower(strings.TrimSpace(tc.Name))
	args := tc.Args
	if args == nil {
		args = map[string]string{}
	}
	pid := strings.TrimSpace(args["project_id"])
	if pid == "" {
		pid = focus
	}
	switch name {
	case "work_briefing":
		return c.gatherWorkBriefing(ctx, refs, pid)
	case "bench_status":
		return c.toolBenchStatus(ctx, refs, pid)
	case "git_status":
		ref, err := c.toolProject(refs, pid)
		if err != nil {
			return err.Error()
		}
		return gitCmd(ref.Path, "status", "--short", "-b")
	case "git_diff":
		ref, err := c.toolProject(refs, pid)
		if err != nil {
			return err.Error()
		}
		return gitCmd(ref.Path, "diff", "--stat", "HEAD")
	case "git_log":
		ref, err := c.toolProject(refs, pid)
		if err != nil {
			return err.Error()
		}
		return gitCmd(ref.Path, "log", "-8", "--oneline", "--decorate")
	case "find_worktrees":
		ref, err := c.toolProject(refs, pid)
		if err != nil {
			return err.Error()
		}
		var b strings.Builder
		b.WriteString(scanQmaWorktrees(ref.Path))
		b.WriteString(featureBranchesBrief(ref.Path))
		if c.engine != nil && c.engine.store != nil {
			runs, _ := c.engine.store.ListRuns(ctx, 15)
			for _, r := range runs {
				if pid != "" && r.ProjectID != pid && r.ProjectID != ref.ID {
					continue
				}
				tasks, _ := c.engine.store.ListTasksByRun(ctx, r.ID)
				for _, t := range tasks {
					if t.WorktreePath != "" {
						b.WriteString(formatLanding(t))
					}
				}
			}
		}
		return b.String()
	case "list_runs":
		if c.engine == nil || c.engine.store == nil {
			return "no store"
		}
		runs, err := c.engine.store.ListRuns(ctx, 12)
		if err != nil {
			return err.Error()
		}
		var b strings.Builder
		for _, r := range runs {
			if pid != "" && r.ProjectID != pid {
				continue
			}
			b.WriteString(fmt.Sprintf("%s [%s] %s\n", r.ID, r.Status, truncateObj(r.Objective, 80)))
		}
		if b.Len() == 0 {
			return "no runs"
		}
		return b.String()
	case "run_detail":
		runID := strings.TrimSpace(args["run_id"])
		if runID == "" || c.engine == nil || c.engine.store == nil {
			return "run_id required"
		}
		r, err := c.engine.store.GetRun(ctx, runID)
		if err != nil {
			return err.Error()
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("run %s status=%s err=%s\nobj=%s\n", r.ID, r.Status, r.Error, r.Objective))
		tasks, _ := c.engine.store.ListTasksByRun(ctx, runID)
		for _, t := range tasks {
			b.WriteString(fmt.Sprintf("- %s [%s] agent=%s branch=%s wt=%s err=%s\n",
				t.ID, t.Status, t.AssignedAgent, t.Branch, t.WorktreePath, truncateObj(t.Error, 80)))
			if ex := taskResultExcerpt(t, 400); ex != "" {
				b.WriteString("  result: " + ex + "\n")
			}
		}
		return b.String()
	case "list_attention":
		return formatLiveSituation(c.gatherBlockedAgents(ctx))
	case "binary_info":
		return localBinaryBrief()
	case "install_qma":
		ref, err := c.toolProject(refs, pid)
		if err != nil {
			return err.Error()
		}
		return installQma(ref.Path)
	case "ask_leader":
		q := strings.TrimSpace(args["question"])
		if q == "" {
			q = "Summarize project state and what to do next."
		}
		reply, err := c.AskLeader(ctx, pid, q, refs)
		if err != nil {
			return err.Error()
		}
		return reply
	case "ask_leaders":
		q := strings.TrimSpace(args["question"])
		if q == "" {
			q = "Summarize project state and what to do next."
		}
		return c.toolAskLeaders(ctx, refs, focus, q, args["project_ids"])
	case "bench_thread":
		ref, err := c.toolProject(refs, pid)
		if err != nil {
			// No focus — try all projects briefly.
			var b strings.Builder
			for _, r := range refs {
				if gp := c.FormatGoalProgress(ctx, r.ID); gp != "" {
					b.WriteString(gp + "\n")
				}
			}
			if b.Len() == 0 {
				return err.Error()
			}
			return b.String()
		}
		gp := c.FormatGoalProgressVerbose(ctx, ref.ID)
		if gp == "" {
			return "no active/recent bench goal for " + ref.Name
		}
		return gp
	case "start_goal", "start_run":
		obj := strings.TrimSpace(args["objective"])
		if obj == "" {
			return "objective required"
		}
		ref, err := c.toolProject(refs, pid)
		if err != nil {
			return err.Error()
		}
		goalID, err := c.StartBenchGoal(ctx, ref.ID, obj, c.forceAgent)
		if err != nil {
			return err.Error()
		}
		return "started bench goal " + goalID
	default:
		return "unknown tool: " + name
	}
}

func (c *Conductor) toolProject(refs []ProjectRef, pid string) (ProjectRef, error) {
	if pid != "" {
		if ref, ok := findProject(refs, pid); ok {
			return ref, nil
		}
	}
	if len(refs) == 1 {
		return refs[0], nil
	}
	return ProjectRef{}, fmt.Errorf("project_id required (ambiguous projects)")
}

func (c *Conductor) toolBenchStatus(ctx context.Context, refs []ProjectRef, pid string) string {
	if c.engine == nil {
		return "no engine"
	}
	benches, err := c.engine.ListBenches(ctx)
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	if len(benches) == 0 {
		b.WriteString("No open benches. Starting a run opens a bench automatically.\n")
	}
	for _, bench := range benches {
		if pid != "" && bench.ProjectID != pid {
			continue
		}
		b.WriteString(fmt.Sprintf("◆ %s [%s] leader=%s status=%s\n", bench.Name, bench.ProjectID, bench.LeaderAgentID, bench.Status))
		if c.engine.store == nil {
			continue
		}
		runs, _ := c.engine.store.ListRuns(ctx, 30)
		var latest *struct {
			id, status, obj string
		}
		for i := range runs {
			r := runs[i]
			if r.ProjectID != bench.ProjectID {
				continue
			}
			latest = &struct{ id, status, obj string }{r.ID, r.Status, r.Objective}
			break
		}
		if latest == nil {
			b.WriteString("  latest run: none\n")
			continue
		}
		b.WriteString(fmt.Sprintf("  latest run: %s [%s] %s\n", latest.id, latest.status, truncateObj(latest.obj, 72)))
		tasks, _ := c.engine.store.ListTasksByRun(ctx, latest.id)
		for _, t := range tasks {
			b.WriteString(fmt.Sprintf("    - %s [%s] agent=%s\n", t.ID, t.Status, orDash(t.AssignedAgent)))
		}
	}
	if pid != "" && b.Len() == 0 {
		return "no bench for project_id=" + pid
	}
	if b.Len() == 0 {
		return "no benches"
	}
	// Also list registered projects without a bench yet.
	seated := map[string]bool{}
	for _, bench := range benches {
		seated[bench.ProjectID] = true
	}
	for _, ref := range refs {
		if pid != "" && ref.ID != pid {
			continue
		}
		if seated[ref.ID] {
			continue
		}
		b.WriteString(fmt.Sprintf("· %s [%s] (bench opens on first run)\n", ref.Name, ref.ID))
	}
	return b.String()
}

func (c *Conductor) toolAskLeaders(ctx context.Context, refs []ProjectRef, focus, question, projectIDsCSV string) string {
	var targets []ProjectRef
	csv := strings.TrimSpace(projectIDsCSV)
	if csv != "" {
		for _, part := range strings.Split(csv, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if ref, ok := findProject(refs, part); ok {
				targets = append(targets, ref)
			}
		}
	}
	if len(targets) == 0 && focus != "" {
		if ref, ok := findProject(refs, focus); ok {
			targets = []ProjectRef{ref}
		}
	}
	if len(targets) == 0 {
		targets = refs
	}
	if len(targets) == 0 {
		return "no projects"
	}
	var b strings.Builder
	for _, ref := range targets {
		reply, err := c.AskLeader(ctx, ref.ID, question, refs)
		b.WriteString("### " + ref.Name + " (" + ref.ID + ")\n")
		if err != nil {
			b.WriteString(err.Error() + "\n\n")
			continue
		}
		b.WriteString(reply + "\n\n")
	}
	return strings.TrimSpace(b.String())
}

func gitCmd(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil && text == "" {
		return err.Error()
	}
	if text == "" {
		return "(empty)"
	}
	return text
}

func installQma(projectPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	before := localBinaryBrief()
	cmd := exec.CommandContext(ctx, "make", "install")
	cmd.Dir = projectPath
	out, err := cmd.CombinedOutput()
	var b strings.Builder
	b.WriteString("## before\n" + before + "\n")
	b.WriteString("## make install\n")
	b.WriteString(truncateObj(string(out), 2000) + "\n")
	if err != nil {
		// Fallback go install
		cmd2 := exec.CommandContext(ctx, "go", "install", "-ldflags",
			"-X github.com/fernandotonon/QtMesh-Agents/pkg/version.Version=0.1.0-dev", "./cmd/qma")
		cmd2.Dir = projectPath
		out2, err2 := cmd2.CombinedOutput()
		b.WriteString("## go install fallback\n")
		b.WriteString(truncateObj(string(out2), 1500) + "\n")
		if err2 != nil {
			b.WriteString("ERROR: " + err2.Error() + "\n")
		}
	}
	b.WriteString("## after\n" + localBinaryBrief())
	b.WriteString("\nNOTE: Restart the daemon to load the new binary:\n  qma daemon stop && qma daemon start\n")
	// Touch a hint file for operators.
	_ = os.WriteFile(filepath.Join(os.TempDir(), "qma-needs-daemon-restart"), []byte(time.Now().Format(time.RFC3339)), 0o600)
	return b.String()
}
