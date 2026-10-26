package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/mock"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/orchestrator"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

func TestHeuristicPlan(t *testing.T) {
	eng := orchestrator.New(orchestrator.Deps{
		Config:  config.Default(),
		Planner: orchestrator.HeuristicPlanner{},
	})
	plan, err := eng.CreatePlan(context.Background(), orchestrator.PlanRequest{
		Objective:     "add logging and improve tests",
		AgentOverride: "mock",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tasks) < 4 {
		t.Fatalf("expected multi-task plan, got %d", len(plan.Tasks))
	}
	for _, task := range plan.Tasks {
		if task.AssignedAgent != "mock" {
			t.Fatalf("override failed: %+v", task)
		}
	}
}

func TestStartRunWithMock(t *testing.T) {
	base := t.TempDir()
	t.Setenv("QTMESH_AGENTS_DATA_DIR", filepath.Join(base, "data"))
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "init")

	script, err := mock.WriteFixtureScript(base)
	if err != nil {
		t.Fatal(err)
	}
	// Headless mock execute doesn't need the interactive script — mock.Execute is in-process.
	_ = script

	store, err := storage.Open(filepath.Join(base, "data", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	reg := agents.NewRegistry()
	reg.Register(mock.New("", nil))

	cfg := config.Default()
	cfg.Execution.UseGitWorktrees = true
	cfg.Execution.MaxConcurrentAgents = 2
	cfg.Execution.TimeoutMinutes = 1
	cfg.Agents.Mock.Enabled = true

	eng := orchestrator.New(orchestrator.Deps{
		Config:   cfg,
		Store:    store,
		Registry: reg,
		Planner:  orchestrator.HeuristicPlanner{},
	})

	ctx := context.Background()
	runID, err := eng.StartRun(ctx, orchestrator.PlanRequest{
		Objective:     "document the repository briefly",
		ProjectID:     "test",
		WorkDir:       repo,
		AgentOverride: "mock",
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		run, err := store.GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == orchestrator.RunSucceeded || run.Status == orchestrator.RunFailed || run.Status == orchestrator.RunCancelled {
			tasks, _ := store.ListTasksByRun(ctx, runID)
			if run.Status != orchestrator.RunSucceeded {
				t.Fatalf("run status=%s err=%s tasks=%+v", run.Status, run.Error, tasks)
			}
			if len(tasks) == 0 {
				t.Fatal("no tasks persisted")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout waiting for run")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}
