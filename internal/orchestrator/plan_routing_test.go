package orchestrator

import (
	"context"
	"testing"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/router"
)

func TestHeuristicImplementationRouting(t *testing.T) {
	for _, tc := range []struct{ objective, category, agent string }{
		{"Fix worker routing and run go test ./...", "implementation", "cursor"},
		{"Fix routing and add unit tests; acceptance: verify all tests pass", "implementation", "cursor"},
		{"Improve distribution and build reliability", "implementation", "cursor"},
		{"Add tests for the backend", "implementation", "cursor"},
		{"Update the TUI and run go test ./...", "frontend", "codex"},
		{"Build a frontend with unit tests", "frontend", "codex"},
		{"Improve UI navigation", "frontend", "codex"},
		{"Update the user interface", "frontend", "codex"},
	} {
		t.Run(tc.objective, func(t *testing.T) {
			plan, err := (HeuristicPlanner{}).Plan(context.Background(), PlanRequest{Objective: tc.objective})
			if err != nil {
				t.Fatal(err)
			}
			r := router.New(config.Default().Routing, map[string]bool{"cursor": true, "codex": true, "claude": true})
			found := false
			for _, task := range plan.Tasks {
				if task.ID != "impl-1" {
					continue
				}
				found = true
				if task.Category != tc.category {
					t.Fatalf("category = %q, want %q", task.Category, tc.category)
				}
				selection, err := r.Select(task.Category)
				if err != nil {
					t.Fatal(err)
				}
				if selection.AgentID != tc.agent {
					t.Fatalf("agent = %q, want %q", selection.AgentID, tc.agent)
				}
			}
			if !found {
				t.Fatal("missing implementation task")
			}
		})
	}
}
