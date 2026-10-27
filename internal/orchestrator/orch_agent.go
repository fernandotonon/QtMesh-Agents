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
// (Cursor preferred). Falls back to interpret/heuristic when the adapter is missing.
func (c *Conductor) agenticChat(ctx context.Context, userText string, refs []ProjectRef, focusProjectID string) (ChatResult, error) {
	if c.adapter == nil {
		return ChatResult{}, fmt.Errorf("no orch agent adapter")
	}
	ok, err := c.adapter.Detect(ctx)
	if err != nil || !ok {
		return ChatResult{}, fmt.Errorf("orch agent unavailable")
	}

	var trace []string
	briefing := c.gatherWorkBriefing(ctx, refs, focusProjectID)
	var last agentTurn

	for i := 0; i < orchAgentMaxIters; i++ {
		prompt := c.orchAgentPrompt(userText, refs, focusProjectID, briefing, trace, i)
		execRes, err := c.adapter.Execute(ctx, agents.AgentTask{
			ID:          fmt.Sprintf("orch-agent-%d-%d", time.Now().UnixNano(), i),
			Description: "QtMesh orchestrator agent turn",
			Prompt:      prompt,
			Timeout:     4 * time.Minute,
		})
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
	for _, act := range last.Actions {
		if act.Type != "run" || act.Objective == "" {
			continue
		}
		ref, ok := findProject(refs, act.ProjectID)
		if !ok && focusProjectID != "" {
			ref, ok = findProject(refs, focusProjectID)
		}
		if !ok && len(refs) == 1 {
			ref, ok = refs[0], true
		}
		if !ok {
			result.Reply += fmt.Sprintf("\n\n(Could not resolve project for run.)")
			continue
		}
		runID, err := c.engine.StartRun(ctx, PlanRequest{
			Objective:     act.Objective,
			ProjectID:     ref.ID,
			WorkDir:       ref.Path,
			AgentOverride: c.forceAgent,
		})
		if err != nil {
			result.Reply += fmt.Sprintf("\n\nFailed to start run on %s: %v", ref.Name, err)
			continue
		}
		act.RunID = runID
		act.ProjectID = ref.ID
		result.Actions = append(result.Actions, act)
		result.Reply += fmt.Sprintf("\n\n▶ Started run `%s` on **%s**: %s", runID, ref.Name, act.Objective)
	}
	return result, nil
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
	return fmt.Sprintf(`You are the QtMesh Agents ORCHESTRATOR — a full agent representing the developer across projects.
You coordinate workers (codex/claude/cursor), inspect git/worktrees/runs, and take actions via tools.
You are NOT a passive chatbot. Investigate with tools, then answer clearly.

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
- start_run: project_id, objective  (prefer actions[] for runs; tool also works)

Rules:
- Prefer tools over guessing. Never invent commits, worktree paths, or run status.
- Worker code lives in .qma-worktrees / feature branches until integrate succeeds.
- When done investigating, set tools:[], fill answer, and optionally actions for new runs.
- Keep answer readable: short verdict first, then bullets, then paths.
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
	case "start_run":
		obj := strings.TrimSpace(args["objective"])
		if obj == "" {
			return "objective required"
		}
		ref, err := c.toolProject(refs, pid)
		if err != nil {
			return err.Error()
		}
		if c.engine == nil {
			return "no engine"
		}
		runID, err := c.engine.StartRun(ctx, PlanRequest{
			Objective: obj, ProjectID: ref.ID, WorkDir: ref.Path, AgentOverride: c.forceAgent,
		})
		if err != nil {
			return err.Error()
		}
		return "started run " + runID
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
