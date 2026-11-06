package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/mock"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

type memProjects struct {
	list []storage.Project
}

func (m memProjects) List(context.Context) ([]storage.Project, error) { return m.list, nil }

func TestMatchProjects(t *testing.T) {
	refs := []ProjectRef{
		{ID: "qtmesheditor", Name: "QtMeshEditor", Path: "/p/QtMeshEditor"},
		{ID: "data-core-clash", Name: "Data Core Clash", Path: "/p/Data-Core-Clash"},
	}
	got := MatchProjects("update QtMeshEditor docs", refs)
	if len(got) != 1 || got[0].ID != "qtmesheditor" {
		t.Fatalf("%+v", got)
	}
	got = MatchProjects("QtMeshEditor and Data Core Clash", refs)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestHeuristicChatStartsRun(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(base + "/db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	reg := agents.NewRegistry()
	reg.Register(mock.New("", nil))
	cfg := config.Default()
	cfg.Agents.Mock.Enabled = true
	eng := New(Deps{Config: cfg, Store: store, Registry: reg, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "DemoApp", Path: base},
	}}, nil)
	c.SetForceAgent("mock")

	res, err := c.Chat(context.Background(), "In DemoApp, add a README note about orchestration")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("actions=%+v reply=%s", res.Actions, res.Reply)
	}
	goalID := res.Actions[0].RunID
	if goalID == "" {
		t.Fatalf("expected goal id, reply=%s", res.Reply)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		g, err := store.GetBenchGoal(context.Background(), goalID)
		if err == nil && (g.Status == storage.GoalSucceeded || g.Status == storage.GoalFailed || g.Status == storage.GoalCancelled) {
			// Without a scripted leader the loop may fail after iteration cap / unit fails;
			// accepting active progress (a unit run exists) is enough for this smoke test.
			_ = c.CancelBenchGoal(context.Background(), goalID, "test done")
			return
		}
		if err == nil && g.Status == storage.GoalActive && g.Iteration > 0 {
			_ = c.CancelBenchGoal(context.Background(), goalID, "test done")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = c.CancelBenchGoal(context.Background(), goalID, "test timeout")
	t.Fatal("timeout waiting for chat-started bench goal progress")
}

func TestSimpleIntentHandlesStatus(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(base + "/db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, nil)
	res, err := c.Chat(context.Background(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 0 {
		t.Fatalf("status should not start runs: %+v", res.Actions)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "orchestrator") {
		t.Fatalf("reply=%q", res.Reply)
	}
}

func TestHeuristicContinueSingleProject(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(base + "/db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	reg := agents.NewRegistry()
	reg.Register(mock.New("", nil))
	cfg := config.Default()
	cfg.Agents.Mock.Enabled = true
	eng := New(Deps{Config: cfg, Store: store, Registry: reg, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "qtmesh-agents", Name: "QtMesh-Agents", Path: base},
	}}, nil)
	c.SetForceAgent("mock")

	res, err := c.Chat(context.Background(), "continue the work you made so far")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("expected continue to start a run, actions=%+v reply=%s", res.Actions, res.Reply)
	}
	if !stringsContainsFold(res.Actions[0].Objective, "continue unfinished") {
		t.Fatalf("expected strengthened continue objective, got %q", res.Actions[0].Objective)
	}
}

func stringsContainsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func TestHeuristicAsksWhenAmbiguous(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(base + "/db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "alpha", Name: "Alpha", Path: base + "/a"},
		{ID: "beta", Name: "Beta", Path: base + "/b"},
	}}, nil)
	res, err := c.Chat(context.Background(), "please implement caching")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 0 {
		t.Fatalf("expected clarify, got %+v reply=%s", res.Actions, res.Reply)
	}
	if res.Reply == "" {
		t.Fatal("empty reply")
	}
}
