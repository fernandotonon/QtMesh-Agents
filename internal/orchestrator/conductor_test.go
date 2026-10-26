package orchestrator

import (
	"context"
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
	if res.Actions[0].RunID == "" {
		t.Fatalf("expected run id, reply=%s", res.Reply)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err := store.GetRun(context.Background(), res.Actions[0].RunID)
		if err == nil && (run.Status == RunSucceeded || run.Status == RunFailed || run.Status == RunCancelled) {
			if run.Status != RunSucceeded {
				t.Fatalf("run status=%s err=%s", run.Status, run.Error)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout waiting for chat-started run")
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
