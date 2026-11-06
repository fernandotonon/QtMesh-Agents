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

func TestIsProgressQuery(t *testing.T) {
	yes := []string{
		"how is the botdeck generation going on Mothership-Raiders?",
		"how is the deckbot generation going?",
		"how's the generation going?",
		"any update on the deckbots?",
		"progress on mothership",
		"status",
		"what's the status of the run?",
	}
	for _, s := range yes {
		if !isProgressQuery(strings.ToLower(s)) {
			t.Fatalf("expected progress query: %q", s)
		}
	}
	no := []string{
		"generate the deckbots",
		"continue the work",
		"implement hair materials",
		"thanks",
		"how is the deckbot generation going? if not running, please continue generating them",
	}
	for _, s := range no {
		if isProgressQuery(strings.ToLower(s)) {
			t.Fatalf("expected not progress: %q", s)
		}
	}
}

func TestFreshWorkNotResumeOnly(t *testing.T) {
	compound := "how is the deckbot generation going on mothership-raiders? if it is not running, please continue generating them. they should be generated one at a time to avoid OOM"
	if !isFreshWorkIntent(compound) {
		t.Fatal("expected fresh work")
	}
	if isResumeOnlyIntent(compound) {
		t.Fatal("must not resume-only")
	}
	if isResumeOnlyIntent("continue") != true {
		t.Fatal("bare continue is resume-only")
	}
	if isFreshWorkIntent("continue") {
		t.Fatal("bare continue is not fresh generate")
	}
}

func TestCompoundGenerateStartsGoalNotResume(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	reg := agents.NewRegistry()
	reg.Register(mock.New("", nil))
	done, _ := json.Marshal(leaderUnitDecision{Done: true, Summary: "ok"})
	lead := &scriptedAdapter{
		BaseAdapter: agents.NewBaseAdapter("claude", "Claude", "true", nil),
		outputs:     []string{string(done)},
	}
	reg.Register(lead)

	cfg := config.Default()
	cfg.Agents.Mock.Enabled = true
	eng := New(Deps{Config: cfg, Store: store, Registry: reg, Planner: HeuristicPlanner{}})
	// Plant an incomplete run that would previously hijack "continue".
	now := time.Now().UTC()
	_ = store.UpsertRun(context.Background(), storage.Run{
		ID: "run-stale", Objective: "old", Status: RunNeedsAttention,
		ProjectID: "mothership-raiders", CreatedAt: now, UpdatedAt: now,
	})
	_ = store.UpsertTask(context.Background(), storage.TaskRecord{
		ID: "run-stale-impl", RunID: "run-stale", ProjectID: "mothership-raiders",
		Description: "impl", Status: "succeeded", CreatedAt: now, UpdatedAt: now,
	})
	_ = store.UpsertTask(context.Background(), storage.TaskRecord{
		ID: "run-stale-verify", RunID: "run-stale", ProjectID: "mothership-raiders",
		Description: "verify", Status: "blocked", CreatedAt: now, UpdatedAt: now,
	})

	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "mothership-raiders", Name: "Mothership-Raiders", Path: base},
	}}, lead)
	c.SetForceAgent("mock")

	msg := "how is the deckbot generation going on mothership-raiders? if it is not running, please continue generating them. they should be generated one at a time to avoid OOM"
	res, err := c.Chat(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(res.Reply)
	if strings.Contains(low, "resuming incomplete") {
		t.Fatalf("must not resume stale verify:\n%s", res.Reply)
	}
	if !strings.Contains(low, "bench goal") && len(res.Actions) == 0 {
		t.Fatalf("expected bench goal:\n%s", res.Reply)
	}
	if len(res.Actions) > 0 {
		_ = c.CancelBenchGoal(context.Background(), res.Actions[0].RunID, "test done")
	}
}

func TestProgressQueryDoesNotHeuristicHelp(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "mothership-raiders", Name: "Mothership-Raiders", Path: base},
		{ID: "other", Name: "Other", Path: base},
	}}, nil)

	cases := []string{
		"how is the botdeck generation going on Mothership-Raiders?",
		"how is the deckbot generation going?",
	}
	for _, msg := range cases {
		res, err := c.Chat(context.Background(), msg)
		if err != nil {
			t.Fatal(err)
		}
		low := strings.ToLower(res.Reply)
		if strings.Contains(low, "happy to help") || strings.Contains(low, "tell me what to build") ||
			strings.Contains(low, "tell me what you want done") {
			t.Fatalf("canned heuristic for %q:\n%s", msg, res.Reply)
		}
		if len(res.Actions) != 0 {
			t.Fatalf("progress query must not start runs: %+v", res.Actions)
		}
		// Clean status: goal snapshot, no-goal, or leader note — never canned help / new runs.
		if !strings.Contains(low, "goal") && !strings.Contains(low, "leader") &&
			!strings.Contains(low, "orchestrator status") && !strings.Contains(low, "no bench") {
			t.Fatalf("expected status content for %q:\n%s", msg, res.Reply)
		}
	}
}

func TestInterrogativeFallbackNoHappyHelp(t *testing.T) {
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

	// Not classified as progress/summary, but still a question — must not canned-help.
	res, err := c.Chat(context.Background(), "is the deckbot feature ready in Demo?")
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(res.Reply)
	if strings.Contains(low, "happy to help") || strings.Contains(low, "tell me what to build") {
		t.Fatalf("interrogative got canned help:\n%s", res.Reply)
	}
}

func TestGenerateDoesNotResumeIncomplete(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	ctx := context.Background()
	now := time.Now().UTC()
	runID := "run-incomplete-1"
	if err := store.UpsertRun(ctx, storage.Run{
		ID: runID, Objective: "prior generation", Status: RunNeedsAttention,
		ProjectID: "demo", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTask(ctx, storage.TaskRecord{
		ID: runID + "-impl", RunID: runID, ProjectID: "demo",
		Description: "impl", Status: "succeeded",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTask(ctx, storage.TaskRecord{
		ID: runID + "-verify", RunID: runID, ProjectID: "demo",
		Description: "verify", Status: "blocked",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, nil)

	res, err := c.Chat(ctx, "generate the deckbots on Demo")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(res.Reply), "resuming incomplete") {
		t.Fatalf("generate must not auto-resume:\n%s", res.Reply)
	}
}

func TestContinueDoesResumeIncomplete(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	ctx := context.Background()
	now := time.Now().UTC()
	runID := "run-incomplete-2"
	if err := store.UpsertRun(ctx, storage.Run{
		ID: runID, Objective: "prior work", Status: RunNeedsAttention,
		ProjectID: "demo", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTask(ctx, storage.TaskRecord{
		ID: runID + "-impl", RunID: runID, ProjectID: "demo",
		Description: "impl", Status: "succeeded",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTask(ctx, storage.TaskRecord{
		ID: runID + "-verify", RunID: runID, ProjectID: "demo",
		Description: "verify", Status: "blocked",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, nil)

	res, err := c.Chat(ctx, "continue on Demo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "resuming incomplete") {
		t.Fatalf("continue should resume:\n%s", res.Reply)
	}
}
