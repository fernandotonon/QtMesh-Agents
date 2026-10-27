package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/events"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

const (
	BenchIdle           = "idle"
	BenchActive         = "active"
	BenchNeedsAttention = "needs_attention"
	BenchClosed         = "closed"

	RoleLeader = "leader"
	RoleWorker = "worker"
)

// EnsureBench opens (or reuses) the working bench for a project and seats a leader + workers.
func (e *Engine) EnsureBench(ctx context.Context, projectID, projectName string, workerAgents []string) (storage.Bench, error) {
	if projectID == "" {
		return storage.Bench{}, fmt.Errorf("project id required for bench")
	}
	if b, err := e.store.GetBenchByProject(ctx, projectID); err == nil {
		_ = e.store.UpsertBench(ctx, storage.Bench{
			ID: b.ID, ProjectID: b.ProjectID, Name: b.Name,
			LeaderAgentID: b.LeaderAgentID, Status: BenchActive,
			CreatedAt: b.CreatedAt, UpdatedAt: time.Now().UTC(),
		})
		b.Status = BenchActive
		return b, nil
	}

	leader := e.preferLeaderAgent(ctx)
	id := fmt.Sprintf("bench-%s", projectID)
	name := projectName
	if name == "" {
		name = projectID
	}
	b := storage.Bench{
		ID:            id,
		ProjectID:     projectID,
		Name:          name + " bench",
		LeaderAgentID: leader,
		Status:        BenchActive,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := e.store.UpsertBench(ctx, b); err != nil {
		return storage.Bench{}, err
	}
	if leader != "" {
		_ = e.store.UpsertBenchMember(ctx, storage.BenchMember{
			BenchID: b.ID, AgentID: leader, Role: RoleLeader,
		})
	}
	seen := map[string]bool{leader: true}
	for _, a := range workerAgents {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		_ = e.store.UpsertBenchMember(ctx, storage.BenchMember{
			BenchID: b.ID, AgentID: a, Role: RoleWorker,
		})
	}
	// Seat common workers even if not yet assigned a task.
	for _, a := range []string{"codex", "claude", "cursor"} {
		if seen[a] {
			continue
		}
		if e.registry != nil {
			if ad, ok := e.registry.Get(a); ok {
				if det, _ := ad.Detect(ctx); det {
					_ = e.store.UpsertBenchMember(ctx, storage.BenchMember{
						BenchID: b.ID, AgentID: a, Role: RoleWorker,
					})
					seen[a] = true
				}
			}
		}
	}
	_ = e.emit(ctx, events.KindBenchOpened, "", "", leader, "opened bench for "+projectID)
	return b, nil
}

func (e *Engine) preferLeaderAgent(ctx context.Context) string {
	if e.registry == nil {
		return "claude"
	}
	for _, id := range []string{"claude", "codex", "cursor"} {
		if a, ok := e.registry.Get(id); ok {
			if det, _ := a.Detect(ctx); det {
				entry, found := e.cfg.AgentByID(id)
				if found && !entry.Enabled {
					continue
				}
				return id
			}
		}
	}
	return "claude"
}

// RaiseAttention records that the user must act, and marks the bench if known.
func (e *Engine) RaiseAttention(ctx context.Context, a storage.Attention) error {
	if a.ID == "" {
		a.ID = fmt.Sprintf("attn-%d", time.Now().UnixNano())
	}
	if a.Status == "" {
		a.Status = "open"
	}
	if a.Kind == "" {
		a.Kind = "user_input"
	}
	if err := e.store.UpsertAttention(ctx, a); err != nil {
		return err
	}
	if a.BenchID != "" {
		if b, err := e.store.GetBench(ctx, a.BenchID); err == nil {
			b.Status = BenchNeedsAttention
			b.UpdatedAt = time.Now().UTC()
			_ = e.store.UpsertBench(ctx, b)
		}
	} else if a.ProjectID != "" {
		if b, err := e.store.GetBenchByProject(ctx, a.ProjectID); err == nil {
			b.Status = BenchNeedsAttention
			b.UpdatedAt = time.Now().UTC()
			_ = e.store.UpsertBench(ctx, b)
			a.BenchID = b.ID
			_ = e.store.UpsertAttention(ctx, a)
		}
	}
	_ = e.emit(ctx, events.KindAttentionRaised, a.RunID, a.TaskID, a.AgentID, a.Message)
	return nil
}

// ResolveAttention clears an attention mark.
func (e *Engine) ResolveAttention(ctx context.Context, id string) error {
	a, err := e.store.GetAttention(ctx, id)
	if err != nil {
		return err
	}
	if err := e.store.ResolveAttention(ctx, id, "resolved"); err != nil {
		return err
	}
	_ = e.healBenchAttention(ctx, a.ProjectID, a.BenchID)
	_ = e.emit(ctx, events.KindAttentionResolved, a.RunID, a.TaskID, a.AgentID, "resolved "+id)
	return nil
}

// healBenchAttention sets the bench back to active when no open attention remains
// for that bench/project (prevents a sticky ⚠ on the project leader).
func (e *Engine) healBenchAttention(ctx context.Context, projectID, benchID string) error {
	open, _ := e.store.ListOpenAttention(ctx)
	if benchID != "" {
		for _, o := range open {
			if o.BenchID == benchID || (o.BenchID == "" && projectID != "" && o.ProjectID == projectID) {
				return nil
			}
		}
		if b, err := e.store.GetBench(ctx, benchID); err == nil {
			b.Status = BenchActive
			b.UpdatedAt = time.Now().UTC()
			return e.store.UpsertBench(ctx, b)
		}
	}
	if projectID == "" {
		return nil
	}
	for _, o := range open {
		if o.ProjectID == projectID {
			return nil
		}
	}
	if b, err := e.store.GetBenchByProject(ctx, projectID); err == nil {
		b.Status = BenchActive
		b.UpdatedAt = time.Now().UTC()
		return e.store.UpsertBench(ctx, b)
	}
	return nil
}

// ListOpenAttention proxies storage.
func (e *Engine) ListOpenAttention(ctx context.Context) ([]storage.Attention, error) {
	return e.store.ListOpenAttention(ctx)
}

// ListBenches proxies storage.
func (e *Engine) ListBenches(ctx context.Context) ([]storage.Bench, error) {
	return e.store.ListBenches(ctx)
}
