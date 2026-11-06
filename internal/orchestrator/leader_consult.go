package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

// LeaderTurn handles a message addressed to the project leader.
// Advisory questions stay one-shot consults; action intents ("go ahead",
// "generate…") resume incomplete runs or start fresh orchestration.
func (c *Conductor) LeaderTurn(ctx context.Context, projectID, message string, refs []ProjectRef) (ChatResult, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return ChatResult{}, fmt.Errorf("empty message")
	}
	ref, ok := findProject(refs, projectID)
	if !ok && projectID == "" && len(refs) == 1 {
		ref = refs[0]
		ok = true
		projectID = ref.ID
	}
	if !ok {
		matched := MatchProjects(message, refs)
		if len(matched) == 1 {
			ref = matched[0]
			projectID = ref.ID
			ok = true
		}
	}
	if !ok && len(refs) == 1 {
		ref = refs[0]
		projectID = ref.ID
		ok = true
	}
	if !ok {
		return ChatResult{}, fmt.Errorf("which project should the leader act on? Select it in the tree or name it")
	}

	if isLeaderActionIntent(message) && c.engine != nil {
		// Resume only on pure continue/unstick — never hijack generate.
		if isResumeOnlyIntent(message) {
			if res, ok := c.tryResumeIncomplete(ctx, message, refs, projectID); ok {
				if res.Reply != "" && !strings.HasPrefix(res.Reply, "[leader:") {
					res.Reply = fmt.Sprintf("[leader:%s on %s]\n%s", c.currentLeaderID(ctx, projectID), ref.Name, res.Reply)
				}
				return res, nil
			}
		}
		// Fall through to full orch chat so workers start (fresh bench goal).
		res, err := c.ChatWith(ctx, message, ChatOpts{ProjectID: projectID})
		if err != nil {
			return ChatResult{}, err
		}
		if res.Reply != "" && !strings.HasPrefix(res.Reply, "[leader:") {
			res.Reply = fmt.Sprintf("[leader:%s on %s]\n%s", c.currentLeaderID(ctx, projectID), ref.Name, res.Reply)
		}
		return res, nil
	}

	reply, err := c.AskLeader(ctx, projectID, message, refs)
	if err != nil {
		return ChatResult{}, err
	}
	return ChatResult{Reply: reply}, nil
}

func (c *Conductor) currentLeaderID(ctx context.Context, projectID string) string {
	if c.engine == nil {
		return "leader"
	}
	if b, err := c.engine.store.GetBenchByProject(ctx, projectID); err == nil && b.LeaderAgentID != "" {
		return b.LeaderAgentID
	}
	return c.engine.preferLeaderAgent(ctx)
}

// tryResumeIncomplete resumes a partially-succeeded run (e.g. verify/integrate
// left blocked after a mid-run ⚠). ok=false means caller should start fresh work.
func (c *Conductor) tryResumeIncomplete(ctx context.Context, message string, refs []ProjectRef, projectID string) (ChatResult, bool) {
	if c.engine == nil {
		return ChatResult{}, false
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		if matched := MatchProjects(message, refs); len(matched) == 1 {
			pid = matched[0].ID
		} else if len(refs) == 1 {
			pid = refs[0].ID
		}
	}
	if pid == "" {
		return ChatResult{}, false
	}
	runID, err := c.engine.LatestIncompleteRunID(ctx, pid)
	if err != nil || runID == "" {
		return ChatResult{}, false
	}
	if err := c.engine.ResumeIncompleteRun(ctx, runID); err != nil {
		return ChatResult{}, false
	}
	name := pid
	if ref, ok := findProject(refs, pid); ok {
		name = ref.Name
	}
	reply := fmt.Sprintf("Resuming incomplete run `%s` on **%s** (unsticking verify/integrate from where workers left off).", runID, name)
	return ChatResult{
		Reply:   reply,
		Actions: []ChatAction{{Type: "run", ProjectID: pid, Objective: "resume " + runID, RunID: runID}},
	}, true
}

// AskLeader asks the project's bench leader a one-shot question with the live
// work briefing. On quota/token exhaustion it walks leadershipFallback
// (claude → cursor → codex by default), reseats the bench, and retries.
// Falls back to a local briefing-based answer when no vendor is usable.
func (c *Conductor) AskLeader(ctx context.Context, projectID, question string, refs []ProjectRef) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("empty question")
	}
	ref, ok := findProject(refs, projectID)
	if !ok && projectID == "" && len(refs) == 1 {
		ref = refs[0]
		ok = true
		projectID = ref.ID
	}
	if !ok {
		matched := MatchProjects(question, refs)
		if len(matched) == 1 {
			ref = matched[0]
			projectID = ref.ID
			ok = true
		}
	}
	if !ok && len(refs) == 1 {
		ref = refs[0]
		projectID = ref.ID
		ok = true
	}
	if !ok {
		return "", fmt.Errorf("which project should the leader advise on? Say ask the leader on <project>")
	}

	briefing := c.gatherWorkBriefing(ctx, refs, projectID)
	candidates := c.leaderCandidates(ctx, projectID)
	if len(candidates) == 0 {
		return fmt.Sprintf("Leader is unavailable. From the live briefing:\n\n%s\n\n(My take: continue the highest-priority unfinished task; resolve any ⚠ attention first.)",
			truncateObj(briefing, 2500)), nil
	}

	prompt := fmt.Sprintf(`You are the project leader for %s (%s).
The human is talking through the QtMesh orchestrator. Answer as the lead: concrete, grounded in the briefing, no invented commits.

LIVE WORK BRIEFING:
%s

Human question:
%s

Reply in plain prose (no JSON). Rules:
- Cite exact worktree paths, feature branches, and commit hashes from the briefing when asked where workers put code.
- Remember: worker commits live in .qma-worktrees / feature branches until integrate succeeds; main checkout can look unchanged.
- Say clearly whether the latest run succeeded or failed, and whether the installed qma binary was updated.
- Be specific about what is blocked and the single next action.`,
		ref.Name, ref.Path, briefing, question)

	var lastErr error
	var lastLeader string
	for _, leaderID := range candidates {
		lastLeader = leaderID
		adapter := c.adapterForID(ctx, leaderID)
		if adapter == nil {
			continue
		}
		exec, err := adapter.Execute(ctx, agents.AgentTask{
			ID:          fmt.Sprintf("leader-consult-%d", time.Now().UnixNano()),
			Description: "Project leader consult",
			WorkDir:     ref.Path,
			Prompt:      prompt,
			Timeout:     3 * time.Minute,
		})
		out := ""
		if exec.Output != "" {
			out = exec.Output
		}
		combined := out
		if err != nil {
			combined = err.Error() + "\n" + out
		}
		if quota, reason := looksLikeQuotaExhausted(combined); quota || (err != nil && looksLikeQuotaErr(err)) {
			if !quota {
				reason = err.Error()
			}
			if c.engine != nil {
				c.engine.MarkAgentExhausted(leaderID, reason)
				_, _ = c.engine.ReseatLeaderForProject(ctx, projectID, reason)
			}
			lastErr = fmt.Errorf("%s: %s", leaderID, reason)
			continue
		}
		if err != nil {
			lastErr = err
			// Soft fallthrough on transport failures too — try next leader.
			continue
		}
		if exec.ExitCode != 0 {
			if quota, reason := looksLikeQuotaExhausted(out); quota {
				if c.engine != nil {
					c.engine.MarkAgentExhausted(leaderID, reason)
					_, _ = c.engine.ReseatLeaderForProject(ctx, projectID, reason)
				}
				lastErr = fmt.Errorf("%s: %s", leaderID, reason)
				continue
			}
			lastErr = fmt.Errorf("leader exit %d", exec.ExitCode)
			continue
		}
		text := strings.TrimSpace(out)
		if text == "" {
			text = "(leader returned empty output)"
		}
		// Prefer trailing assistant content if the CLI echoes the prompt.
		if i := strings.LastIndex(strings.ToLower(text), "\nassistant\n"); i >= 0 {
			text = strings.TrimSpace(text[i+len("\nassistant\n"):])
		}
		return fmt.Sprintf("[leader:%s on %s]\n%s", leaderID, ref.Name, text), nil
	}

	if lastErr != nil {
		return fmt.Sprintf("Leader call failed after trying %v (%v). Briefing-based answer:\n\n%s",
			candidates, lastErr, truncateObj(briefing, 2000)), nil
	}
	return fmt.Sprintf("Leader (%s) is unavailable. From the live briefing:\n\n%s\n\n(My take: continue the highest-priority unfinished task; resolve any ⚠ attention first.)",
		lastLeader, truncateObj(briefing, 2500)), nil
}

func looksLikeQuotaErr(err error) bool {
	if err == nil {
		return false
	}
	ok, _ := looksLikeQuotaExhausted(err.Error())
	return ok
}

// leaderCandidates returns the ordered list of agents to try for leadership:
// current bench leader first (if still viable), then the rest of leadershipFallback.
func (c *Conductor) leaderCandidates(ctx context.Context, projectID string) []string {
	order := []string{"claude", "cursor", "codex"}
	if c.engine != nil {
		order = c.engine.LeadershipOrder()
	}
	var seated string
	if c.engine != nil && projectID != "" {
		if bench, err := c.engine.store.GetBenchByProject(ctx, projectID); err == nil {
			seated = bench.LeaderAgentID
		}
	}
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		if c.engine != nil && c.engine.IsAgentExhausted(id) {
			return
		}
		if c.adapterForID(ctx, id) == nil {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	add(seated)
	for _, id := range order {
		add(id)
	}
	return out
}

func (c *Conductor) adapterForID(ctx context.Context, id string) agents.AgentAdapter {
	if id == "" {
		return nil
	}
	if c.engine != nil && c.engine.registry != nil {
		if ad, ok := c.engine.registry.Get(id); ok {
			if det, err := ad.Detect(ctx); err == nil && det {
				return ad
			}
		}
	}
	if c.adapter != nil && c.adapter.ID() == id {
		if det, err := c.adapter.Detect(ctx); err == nil && det {
			return c.adapter
		}
	}
	return nil
}

func (c *Conductor) leaderAdapter(ctx context.Context, leaderID string) agents.AgentAdapter {
	if ad := c.adapterForID(ctx, leaderID); ad != nil {
		return ad
	}
	for _, id := range c.leaderCandidates(ctx, "") {
		if ad := c.adapterForID(ctx, id); ad != nil {
			return ad
		}
	}
	if c.adapter != nil {
		if det, err := c.adapter.Detect(ctx); err == nil && det {
			return c.adapter
		}
	}
	return nil
}
