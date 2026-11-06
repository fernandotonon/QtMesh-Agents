package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/scheduler"
)

// Planner produces a structured task plan from an objective.
type Planner interface {
	Name() string
	Plan(ctx context.Context, req PlanRequest) (scheduler.Plan, error)
}

// HeuristicPlanner builds a deterministic DAG without calling a provider.
type HeuristicPlanner struct{}

func (HeuristicPlanner) Name() string { return "heuristic" }

func (HeuristicPlanner) Plan(ctx context.Context, req PlanRequest) (scheduler.Plan, error) {
	_ = ctx
	obj := strings.TrimSpace(req.Objective)
	if obj == "" {
		return scheduler.Plan{}, fmt.Errorf("objective required")
	}

	parts := splitObjective(obj)
	// Project leader organizes the bench before workers execute.
	tasks := []scheduler.Task{
		{
			ID:          "lead",
			Description: "As project leader, break down the objective, name risks, and assign focus for the team: " + obj,
			Category:    "leadership",
			Acceptance:  []string{"Clear work breakdown written", "Priorities and risks listed for workers"},
			MaxRetries:  1,
			Status:      scheduler.StatusPending,
			ProjectID:   req.ProjectID,
		},
		{
			ID:           "discover",
			Description:  "Analyze the repository and clarify scope for: " + obj,
			Category:     "architecture",
			Dependencies: []string{"lead"},
			Acceptance:   []string{"Key packages and constraints identified", "Implementation approach outlined"},
			MaxRetries:   2,
			Status:       scheduler.StatusPending,
			ProjectID:    req.ProjectID,
		},
	}

	var implIDs []string
	for i, part := range parts {
		id := fmt.Sprintf("impl-%d", i+1)
		implIDs = append(implIDs, id)
		cat := "implementation"
		lower := strings.ToLower(part)
		switch {
		case regexp.MustCompile(`\b(ui|tui|frontend)\b|user interface`).MatchString(lower):
			cat = "frontend"
		case strings.Contains(lower, "review"):
			cat = "review"
		}
		tasks = append(tasks, scheduler.Task{
			ID:           id,
			Description:  part,
			Category:     cat,
			Dependencies: []string{"discover"},
			Acceptance:   []string{"Scoped changes applied in isolation", "Diff is non-empty or explicit no-op justified"},
			MaxRetries:   2,
			Status:       scheduler.StatusPending,
			ProjectID:    req.ProjectID,
		})
	}

	tasks = append(tasks, scheduler.Task{
		ID:           "verify",
		Description:  "Run verification (tests/build where applicable) and summarize results for: " + obj,
		Category:     "testing",
		Dependencies: append([]string{}, implIDs...),
		Acceptance:   []string{"Verification commands attempted", "Failures reported clearly"},
		MaxRetries:   2,
		Status:       scheduler.StatusPending,
		ProjectID:    req.ProjectID,
	})

	tasks = append(tasks, scheduler.Task{
		ID:           "integrate",
		Description:  "Collect worktree diffs and prepare an integration summary (do not merge protected branches)",
		Category:     "review",
		Dependencies: []string{"verify"},
		Acceptance:   []string{"Per-task diffs listed", "Human approval required before merge"},
		MaxRetries:   1,
		Status:       scheduler.StatusPending,
		ProjectID:    req.ProjectID,
	})

	plan := scheduler.Plan{Tasks: tasks}
	if err := plan.Validate(); err != nil {
		return scheduler.Plan{}, err
	}
	return plan, nil
}

// UnitPlan builds a focused impl→verify plan for one bench-loop work unit
// (skips lead/discover/integrate so the durable goal loop owns completion).
func UnitPlan(req PlanRequest) (scheduler.Plan, error) {
	obj := strings.TrimSpace(req.Objective)
	if obj == "" {
		return scheduler.Plan{}, fmt.Errorf("objective required")
	}
	tasks := []scheduler.Task{
		{
			ID:          "impl-1",
			Description: obj,
			Category:    "implementation",
			Acceptance:  []string{"Scoped changes applied in isolation", "Diff is non-empty or explicit no-op justified"},
			MaxRetries:  3,
			Status:      scheduler.StatusPending,
			ProjectID:   req.ProjectID,
		},
		{
			ID:           "verify",
			Description:  "Verify the unit of work: " + obj,
			Category:     "testing",
			Dependencies: []string{"impl-1"},
			Acceptance:   []string{"Verification commands attempted", "Failures reported clearly"},
			MaxRetries:   2,
			Status:       scheduler.StatusPending,
			ProjectID:    req.ProjectID,
		},
	}
	plan := scheduler.Plan{Tasks: tasks}
	if err := plan.Validate(); err != nil {
		return scheduler.Plan{}, err
	}
	return plan, nil
}

func splitObjective(obj string) []string {
	// Only split on explicit parallel markers. Do NOT split on numbered lists —
	// conductors often emit "1) … 2) …" as a single sequential plan.
	re := regexp.MustCompile(`(?i)\s+;\s+|\s+and also\s+`)
	parts := re.Split(obj, -1)
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Drop tiny fragments that are not actionable work items.
		if len(p) < 12 {
			continue
		}
		out = append(out, p)
	}
	if len(out) <= 1 {
		return []string{"Implement: " + obj}
	}
	if len(out) > 3 {
		out = out[:3]
	}
	for i := range out {
		if !strings.HasPrefix(strings.ToLower(out[i]), "implement") {
			out[i] = "Implement: " + out[i]
		}
	}
	return out
}

// ProviderPlanner asks a coding agent to emit a JSON plan, with heuristic fallback.
type ProviderPlanner struct {
	Adapter  agents.AgentAdapter
	Fallback Planner
}

func (p ProviderPlanner) Name() string {
	if p.Adapter == nil {
		return "provider"
	}
	return "provider:" + p.Adapter.ID()
}

func (p ProviderPlanner) Plan(ctx context.Context, req PlanRequest) (scheduler.Plan, error) {
	if p.Adapter == nil {
		return p.Fallback.Plan(ctx, req)
	}
	prompt := planningPrompt(req)
	exec, err := p.Adapter.Execute(ctx, agents.AgentTask{
		ID:          "plan-" + req.ProjectID,
		Description: "Create orchestration plan",
		WorkDir:     req.WorkDir,
		Prompt:      prompt,
	})
	if err != nil || exec.ExitCode != 0 {
		if p.Fallback != nil {
			return p.Fallback.Plan(ctx, req)
		}
		if err != nil {
			return scheduler.Plan{}, err
		}
		return scheduler.Plan{}, fmt.Errorf("planner exit %d: %s", exec.ExitCode, trimOut(exec.Output))
	}
	plan, err := parsePlanJSON(exec.Output, req.ProjectID)
	if err != nil {
		if p.Fallback != nil {
			return p.Fallback.Plan(ctx, req)
		}
		return scheduler.Plan{}, err
	}
	if err := plan.Validate(); err != nil {
		if p.Fallback != nil {
			return p.Fallback.Plan(ctx, req)
		}
		return scheduler.Plan{}, err
	}
	return plan, nil
}

func planningPrompt(req PlanRequest) string {
	tree := ""
	if req.WorkDir != "" {
		tree = shallowTree(req.WorkDir, 2)
	}
	return fmt.Sprintf(`You are the QtMesh Agents planning orchestrator.
Produce a JSON task plan for this development objective. Output ONLY JSON, no markdown.

Objective: %s
Project: %s
WorkDir: %s
Repo sketch:
%s

JSON schema:
{
  "tasks": [
    {
      "id": "unique-slug",
      "description": "what to do",
      "category": "architecture|implementation|frontend|testing|review",
      "dependencies": ["other-id"],
      "acceptance": ["criterion"],
      "max_retries": 2
    }
  ]
}

Rules:
- 3 to 8 tasks
- DAG only, no cycles
- Include analyze/discover before implementation
- End with verify and an integrate/review task that does NOT merge protected branches
- Keep tasks narrowly scoped for parallel work where possible
`, req.Objective, req.ProjectID, req.WorkDir, tree)
}

type planJSON struct {
	Tasks []struct {
		ID           string   `json:"id"`
		Description  string   `json:"description"`
		Category     string   `json:"category"`
		Dependencies []string `json:"dependencies"`
		Acceptance   []string `json:"acceptance"`
		MaxRetries   int      `json:"max_retries"`
	} `json:"tasks"`
}

func parsePlanJSON(raw, projectID string) (scheduler.Plan, error) {
	raw = strings.TrimSpace(raw)
	// Extract first JSON object if the model wrapped it.
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var pj planJSON
	if err := json.Unmarshal([]byte(raw), &pj); err != nil {
		return scheduler.Plan{}, fmt.Errorf("parse plan json: %w", err)
	}
	var tasks []scheduler.Task
	for _, t := range pj.Tasks {
		mr := t.MaxRetries
		if mr <= 0 {
			mr = 2
		}
		tasks = append(tasks, scheduler.Task{
			ID:           t.ID,
			Description:  t.Description,
			Category:     t.Category,
			Dependencies: t.Dependencies,
			Acceptance:   t.Acceptance,
			MaxRetries:   mr,
			Status:       scheduler.StatusPending,
			ProjectID:    projectID,
		})
	}
	return scheduler.Plan{Tasks: tasks}, nil
}

func shallowTree(root string, depth int) string {
	var b strings.Builder
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, ".git") || strings.HasPrefix(rel, "bin/") || strings.Contains(rel, "node_modules") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Count(rel, string(os.PathSeparator)) >= depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		b.WriteString(rel)
		b.WriteByte('\n')
		if b.Len() > 2000 {
			return fmt.Errorf("stop")
		}
		return nil
	})
	return b.String()
}

func trimOut(s string) string {
	const max = 32_000
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	// Keep the tail — final answers and latest progress matter more than the banner.
	return "…" + s[len(s)-max:]
}
