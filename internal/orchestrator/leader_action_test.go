package orchestrator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

func TestIsLeaderActionIntent(t *testing.T) {
	actions := []string{
		"go ahead",
		"go ahead, then generate the deckbots",
		"generate the deckbots",
		"continue",
		"resume verify",
		"unstick the run",
		"please do it",
	}
	for _, s := range actions {
		if !isLeaderActionIntent(s) {
			t.Fatalf("expected action intent: %q", s)
		}
	}
	advice := []string{
		"did u generate the deckbots again?",
		"where are the models?",
		"what should we do next?",
		"status",
		"generated?",
		"do not generate the hover again! only the other 2",
	}
	for _, s := range advice {
		if isLeaderActionIntent(s) {
			t.Fatalf("expected advisory, not action: %q", s)
		}
	}
}

func TestGeneratedQuestionNotFreshWork(t *testing.T) {
	for _, s := range []string{"generated?", "was another deckbot generated?", "Generated?"} {
		if isFreshWorkIntent(s) {
			t.Fatalf("must not be fresh work: %q", s)
		}
		if !isGenerationStatusAsk(strings.ToLower(s)) {
			t.Fatalf("expected generation status ask: %q", s)
		}
	}
}

func TestDoNotGenerateIsSteerNotFresh(t *testing.T) {
	msg := "do not generate the hover again! only the other 2"
	if isFreshWorkIntent(msg) {
		t.Fatal("negated generate must not be fresh work")
	}
	if !isGoalSteerIntent(msg) {
		t.Fatal("expected goal steer")
	}
}

func TestSteerAmendsActiveGoal(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "mothership-raiders", Name: "Mothership-Raiders", Path: base},
	}}, nil)
	ctx := context.Background()

	goalID, err := c.StartBenchGoal(ctx, "mothership-raiders",
		"generate hover + walker + flyer one at a time", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.CancelBenchGoal(ctx, goalID, "pause loop for test")
	// Re-open as active without a live loop: plant status.
	g, err := store.GetBenchGoal(ctx, goalID)
	if err != nil {
		t.Fatal(err)
	}
	g.Status = storage.GoalActive
	g.UpdatedAt = time.Now().UTC()
	if err := store.UpsertBenchGoal(ctx, g); err != nil {
		t.Fatal(err)
	}

	res, err := c.ChatWith(ctx, "do not generate the hover again! only the other 2", ChatOpts{
		ProjectID: "mothership-raiders",
	})
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(res.Reply)
	if strings.Contains(low, "started") && strings.Contains(low, "goal-") {
		t.Fatalf("must amend, not start new goal:\n%s", res.Reply)
	}
	if !strings.Contains(low, "updated") && !strings.Contains(low, "correction") && !strings.Contains(low, "other 2") {
		t.Fatalf("expected amend ack:\n%s", res.Reply)
	}
	g2, err := store.GetBenchGoal(ctx, goalID)
	if err != nil {
		t.Fatal(err)
	}
	if g2.ID != goalID {
		t.Fatalf("goal id changed")
	}
	if !strings.Contains(strings.ToLower(g2.Objective), "other 2") &&
		!strings.Contains(strings.ToLower(g2.Objective), "do not generate the hover") {
		t.Fatalf("objective not amended: %q", g2.Objective)
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(g2.Objective)), "generate the hover") {
		t.Fatalf("sliced into hover regenerate: %q", g2.Objective)
	}
}

func TestGeneratedQuestionDoesNotStartGoal(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "mothership-raiders", Name: "Mothership-Raiders", Path: base},
	}}, nil)

	res, err := c.ChatWith(context.Background(), "generated?", ChatOpts{ProjectID: "mothership-raiders"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 0 {
		t.Fatalf("must not start goal: %+v reply=%s", res.Actions, res.Reply)
	}
	if strings.Contains(strings.ToLower(res.Reply), "started") {
		t.Fatalf("must not start goal:\n%s", res.Reply)
	}
}

func TestAuthRequiredHardNeedsYou(t *testing.T) {
	out := "assistant\nError: Authentication required. Please run 'agent login' first.\n"
	ok, msg := looksLikeHardNeedsYou(out, 1)
	if !ok {
		t.Fatal("expected hard Needs you for agent login auth")
	}
	if msg == "" {
		t.Fatal("empty reason")
	}
}

func TestChatRepliesStayBrief(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "mothership-raiders", Name: "Mothership-Raiders", Path: base},
	}}, nil)
	ctx := context.Background()
	goalID, err := c.StartBenchGoal(ctx, "mothership-raiders", "generate walker only", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.CancelBenchGoal(ctx, goalID, "test")
	g, _ := store.GetBenchGoal(ctx, goalID)
	g.Status = storage.GoalActive
	_ = store.UpsertBenchGoal(ctx, g)

	res, err := c.ChatWith(ctx, "generate the deckbots", ChatOpts{ProjectID: "mothership-raiders"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Reply, "## Recent thread") || strings.Contains(res.Reply, "Latest unit run") {
		t.Fatalf("chat spam:\n%s", res.Reply)
	}
	if strings.Count(res.Reply, "\n") > 4 {
		t.Fatalf("reply too long:\n%s", res.Reply)
	}
}
