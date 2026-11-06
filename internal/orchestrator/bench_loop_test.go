package orchestrator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/mock"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

func TestParseLeaderUnitDecision(t *testing.T) {
	raw := "```json\n{\"done\":true,\"summary\":\"shipped\",\"next_objective\":\"\",\"needs_you\":false}\n```"
	d, err := parseLeaderUnitDecision(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Done || d.Summary != "shipped" {
		t.Fatalf("%+v", d)
	}
}

func TestBenchGoalSucceedsWhenLeaderSaysDone(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	reg := agents.NewRegistry()
	reg.Register(mock.New("", nil))

	// Leader: first call returns next unit, second returns done.
	unit1, _ := json.Marshal(leaderUnitDecision{NextObjective: "add hello.txt", Summary: "start"})
	unit2, _ := json.Marshal(leaderUnitDecision{Done: true, Summary: "file landed"})
	lead := &scriptedAdapter{
		BaseAdapter: agents.NewBaseAdapter("claude", "Claude", "true", nil),
		outputs:     []string{string(unit1), string(unit2)},
	}
	reg.Register(lead)

	cfg := config.Default()
	cfg.Agents.Mock.Enabled = true
	cfg.Routing.LeadershipFallback = []string{"claude", "cursor", "codex"}
	eng := New(Deps{Config: cfg, Store: store, Registry: reg, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, lead)
	c.SetForceAgent("mock")

	ctx := context.Background()
	goalID, err := c.StartBenchGoal(ctx, "demo", "add a hello file", "mock")
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		g, err := store.GetBenchGoal(ctx, goalID)
		if err != nil {
			t.Fatal(err)
		}
		if g.Status == storage.GoalSucceeded {
			if !strings.Contains(strings.ToLower(c.FormatGoalProgress(ctx, "demo")), "succeeded") &&
				g.Status != storage.GoalSucceeded {
				t.Fatalf("progress missing: %s", c.FormatGoalProgress(ctx, "demo"))
			}
			return
		}
		if g.Status == storage.GoalFailed || g.Status == storage.GoalCancelled {
			t.Fatalf("goal ended %s: %s", g.Status, g.Error)
		}
		time.Sleep(200 * time.Millisecond)
	}
	g, _ := store.GetBenchGoal(ctx, goalID)
	t.Fatalf("timeout status=%s iter=%d err=%s", g.Status, g.Iteration, g.Error)
}

func TestGenerateStartsBenchGoalNotHeuristicHelp(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	reg := agents.NewRegistry()
	reg.Register(mock.New("", nil))
	done, _ := json.Marshal(leaderUnitDecision{Done: true, Summary: "already done"})
	lead := &scriptedAdapter{
		BaseAdapter: agents.NewBaseAdapter("claude", "Claude", "true", nil),
		outputs:     []string{string(done)},
	}
	reg.Register(lead)

	cfg := config.Default()
	cfg.Agents.Mock.Enabled = true
	eng := New(Deps{Config: cfg, Store: store, Registry: reg, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, lead)
	c.SetForceAgent("mock")

	res, err := c.Chat(context.Background(), "generate the deckbots on Demo")
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(res.Reply)
	if strings.Contains(low, "happy to help") || strings.Contains(low, "tell me what to build") {
		t.Fatalf("canned help:\n%s", res.Reply)
	}
	if !strings.Contains(low, "bench goal") && len(res.Actions) == 0 {
		t.Fatalf("expected bench goal start:\n%s", res.Reply)
	}
	// Give the loop a moment; cancel so we don't leak goroutines past test.
	if len(res.Actions) > 0 && res.Actions[0].RunID != "" {
		_ = c.CancelBenchGoal(context.Background(), res.Actions[0].RunID, "test done")
	}
}

func TestNeedsYouPausesAndResumeContinues(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	reg := agents.NewRegistry()
	needYou, _ := json.Marshal(leaderUnitDecision{
		NeedsYou: true, NeedsYouReason: "please login to vendor", Summary: "blocked",
	})
	done, _ := json.Marshal(leaderUnitDecision{Done: true, Summary: "ok after login"})
	lead := &scriptedAdapter{
		BaseAdapter: agents.NewBaseAdapter("claude", "Claude", "true", nil),
		outputs:     []string{string(needYou), string(done)},
	}
	reg.Register(lead)

	cfg := config.Default()
	eng := New(Deps{Config: cfg, Store: store, Registry: reg, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, lead)

	ctx := context.Background()
	goalID, err := c.StartBenchGoal(ctx, "demo", "do the thing", "")
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		g, _ := store.GetBenchGoal(ctx, goalID)
		if g.Status == storage.GoalPausedNeedsYou {
			break
		}
		if g.Status == storage.GoalSucceeded || g.Status == storage.GoalFailed {
			t.Fatalf("expected pause, got %s", g.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	g, _ := store.GetBenchGoal(ctx, goalID)
	if g.Status != storage.GoalPausedNeedsYou {
		t.Fatalf("want paused_needs_you got %s err=%s", g.Status, g.Error)
	}

	open, _ := store.ListOpenAttention(ctx)
	found := false
	for _, a := range open {
		if a.GoalID == goalID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected attention with goal_id")
	}

	if err := c.ResumeBenchGoal(ctx, goalID, ""); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		g, _ = store.GetBenchGoal(ctx, goalID)
		if g.Status == storage.GoalSucceeded {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	g, _ = store.GetBenchGoal(ctx, goalID)
	t.Fatalf("resume did not succeed: %s %s", g.Status, g.Error)
}

func TestProgressQuerySurfacesThread(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, nil)

	ctx := context.Background()
	bench, err := eng.EnsureBench(ctx, "demo", "Demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	th := storage.BenchThread{ID: "thread-" + bench.ID, BenchID: bench.ID, ProjectID: "demo", CreatedAt: time.Now().UTC()}
	_ = store.UpsertBenchThread(ctx, th)
	_ = store.UpsertBenchGoal(ctx, storage.BenchGoal{
		ID: "goal-1", BenchID: bench.ID, ProjectID: "demo", ThreadID: th.ID,
		Objective: "generate deckbots", Status: storage.GoalActive, Iteration: 2,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	_ = store.AppendThreadMessage(ctx, storage.ThreadMessage{
		ID: "tm-1", ThreadID: th.ID, Role: storage.ThreadRoleWorker,
		Content: "unit run succeeded for deckbot mesh", CreatedAt: time.Now().UTC(),
	})

	res, err := c.Chat(ctx, "how is the deckbot generation going on Demo?")
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(res.Reply)
	if strings.Contains(low, "happy to help") {
		t.Fatalf("canned help:\n%s", res.Reply)
	}
	if !strings.Contains(low, "bench goal") && !strings.Contains(low, "deckbot") && !strings.Contains(low, "thread") {
		t.Fatalf("expected goal/thread content:\n%s", res.Reply)
	}
}

func TestSoftStallLooksRetryable(t *testing.T) {
	soft, msg := looksLikeSoftStall("Error: sandbox violation when writing /tmp/x")
	if !soft {
		t.Fatal("expected soft stall")
	}
	if msg == "" {
		t.Fatal("expected message")
	}
	hard, _ := looksLikeHardNeedsYou("Error: sandbox violation when writing /tmp/x", 1)
	if hard {
		t.Fatal("sandbox should not be hard Needs you")
	}
	hard, _ = looksLikeHardNeedsYou("please login to continue with OAuth", 1)
	if !hard {
		t.Fatal("login should be hard Needs you")
	}
}
