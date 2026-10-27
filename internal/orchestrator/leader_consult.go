package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

// AskLeader asks the project's bench leader (prefer Claude) a one-shot question
// with the live work briefing. Falls back to a local briefing-based answer.
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
	leaderID := "claude"
	if c.engine != nil {
		if bench, err := c.engine.store.GetBenchByProject(ctx, projectID); err == nil && bench.LeaderAgentID != "" {
			leaderID = bench.LeaderAgentID
		}
	}

	adapter := c.leaderAdapter(ctx, leaderID)
	if adapter == nil {
		return fmt.Sprintf("Leader (%s) is unavailable. From the live briefing:\n\n%s\n\n(My take: continue the highest-priority unfinished task; resolve any ⚠ attention first.)",
			leaderID, truncateObj(briefing, 2500)), nil
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

	exec, err := adapter.Execute(ctx, agents.AgentTask{
		ID:          fmt.Sprintf("leader-consult-%d", time.Now().UnixNano()),
		Description: "Project leader consult",
		WorkDir:     ref.Path,
		Prompt:      prompt,
		Timeout:     3 * time.Minute,
	})
	if err != nil {
		return fmt.Sprintf("Leader call failed (%v). Briefing-based answer:\n\n%s", err, truncateObj(briefing, 2000)), nil
	}
	out := strings.TrimSpace(exec.Output)
	if out == "" {
		out = "(leader returned empty output)"
	}
	// Prefer trailing assistant content if the CLI echoes the prompt.
	if i := strings.LastIndex(strings.ToLower(out), "\nassistant\n"); i >= 0 {
		out = strings.TrimSpace(out[i+len("\nassistant\n"):])
	}
	return fmt.Sprintf("[leader:%s on %s]\n%s", leaderID, ref.Name, out), nil
}

func (c *Conductor) leaderAdapter(ctx context.Context, leaderID string) agents.AgentAdapter {
	if c.engine != nil && c.engine.registry != nil {
		if ad, ok := c.engine.registry.Get(leaderID); ok {
			if det, err := ad.Detect(ctx); err == nil && det {
				return ad
			}
		}
		// Prefer claude if named leader missing.
		if leaderID != "claude" {
			if ad, ok := c.engine.registry.Get("claude"); ok {
				if det, err := ad.Detect(ctx); err == nil && det {
					return ad
				}
			}
		}
	}
	if c.adapter != nil {
		if det, err := c.adapter.Detect(ctx); err == nil && det {
			return c.adapter
		}
	}
	return nil
}
