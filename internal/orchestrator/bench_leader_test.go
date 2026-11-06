package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

func stubAgent(id string) *scriptedAdapter {
	return &scriptedAdapter{
		BaseAdapter: agents.NewBaseAdapter(id, id, "true", nil),
	}
}

func TestPreferLeaderSkipsExhausted(t *testing.T) {
	cfg := config.Default()
	cfg.Routing.LeadershipFallback = []string{"claude", "cursor", "codex"}
	reg := agents.NewRegistry()
	reg.Register(stubAgent("claude"))
	reg.Register(stubAgent("cursor"))
	reg.Register(stubAgent("codex"))
	e := New(Deps{Config: cfg, Registry: reg})

	got := e.preferLeaderAgent(context.Background())
	if got != "claude" {
		t.Fatalf("expected claude first, got %q", got)
	}
	e.MarkAgentExhausted("claude", "no tokens")
	got = e.preferLeaderAgent(context.Background())
	if got != "cursor" {
		t.Fatalf("expected cursor after claude exhausted, got %q", got)
	}
	e.MarkAgentExhausted("cursor", "no tokens")
	got = e.preferLeaderAgent(context.Background())
	if got != "codex" {
		t.Fatalf("expected codex after cursor exhausted, got %q", got)
	}
}

func TestReseatLeaderPersists(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.Default()
	e := New(Deps{Config: cfg, Store: store, Registry: agents.NewRegistry()})
	ctx := context.Background()
	b := storage.Bench{
		ID: "bench-p1", ProjectID: "p1", Name: "p1 bench",
		LeaderAgentID: "claude", Status: BenchActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.UpsertBench(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := e.ReseatLeader(ctx, b.ID, "cursor", "claude out of tokens"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBench(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LeaderAgentID != "cursor" {
		t.Fatalf("leader=%q want cursor", got.LeaderAgentID)
	}
}

func TestEnsureBenchReseatsExhaustedLeader(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.Default()
	reg := agents.NewRegistry()
	reg.Register(stubAgent("claude"))
	reg.Register(stubAgent("cursor"))
	reg.Register(stubAgent("codex"))
	e := New(Deps{Config: cfg, Store: store, Registry: reg})
	ctx := context.Background()
	b := storage.Bench{
		ID: "bench-p2", ProjectID: "p2", Name: "p2 bench",
		LeaderAgentID: "claude", Status: BenchActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.UpsertBench(ctx, b); err != nil {
		t.Fatal(err)
	}
	e.MarkAgentExhausted("claude", "no tokens")
	got, err := e.EnsureBench(ctx, "p2", "p2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.LeaderAgentID != "cursor" {
		t.Fatalf("expected reseat to cursor, got %q", got.LeaderAgentID)
	}
}

func TestLeadershipOrderDefault(t *testing.T) {
	e := New(Deps{Config: config.Default()})
	order := e.LeadershipOrder()
	if len(order) != 3 || order[0] != "claude" || order[1] != "cursor" || order[2] != "codex" {
		t.Fatalf("got %v", order)
	}
}
