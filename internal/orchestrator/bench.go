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
		leader := b.LeaderAgentID
		// Sticky leader stays unless marked exhausted — then reseat along leadershipFallback.
		if leader == "" || e.IsAgentExhausted(leader) || !e.leaderAvailable(ctx, leader) {
			if next := e.preferLeaderAgent(ctx); next != "" && next != leader {
				_ = e.ReseatLeader(ctx, b.ID, next, "previous leader unavailable or out of tokens")
				b.LeaderAgentID = next
				leader = next
			}
		}
		_ = e.store.UpsertBench(ctx, storage.Bench{
			ID: b.ID, ProjectID: b.ProjectID, Name: b.Name,
			LeaderAgentID: leader, Status: BenchActive,
			CreatedAt: b.CreatedAt, UpdatedAt: time.Now().UTC(),
		})
		b.LeaderAgentID = leader
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

// ReseatLeader updates the bench's leader agent and emits a reseat event.
func (e *Engine) ReseatLeader(ctx context.Context, benchID, newLeader, reason string) error {
	if benchID == "" || newLeader == "" {
		return fmt.Errorf("bench id and new leader required")
	}
	b, err := e.store.GetBench(ctx, benchID)
	if err != nil {
		return err
	}
	prev := b.LeaderAgentID
	b.LeaderAgentID = newLeader
	b.UpdatedAt = time.Now().UTC()
	if err := e.store.UpsertBench(ctx, b); err != nil {
		return err
	}
	// Demote any prior leader-role seats so the roster has exactly one leader.
	if members, err := e.store.ListBenchMembers(ctx, b.ID); err == nil {
		for _, m := range members {
			if m.AgentID == newLeader {
				continue
			}
			if m.Role == RoleLeader || m.AgentID == prev {
				_ = e.store.UpsertBenchMember(ctx, storage.BenchMember{
					BenchID: b.ID, AgentID: m.AgentID, Role: RoleWorker,
				})
			}
		}
	}
	_ = e.store.UpsertBenchMember(ctx, storage.BenchMember{
		BenchID: b.ID, AgentID: newLeader, Role: RoleLeader,
	})
	if prev == newLeader {
		return nil
	}
	msg := fmt.Sprintf("leader %s → %s", prev, newLeader)
	if reason != "" {
		msg += ": " + reason
	}
	_ = e.emit(ctx, events.KindLeaderReseated, "", "", newLeader, msg)
	e.log.Info("bench leader reseated", "bench", benchID, "from", prev, "to", newLeader, "reason", reason)
	return nil
}

// ReseatLeaderForProject picks the next available leader when current is exhausted.
func (e *Engine) ReseatLeaderForProject(ctx context.Context, projectID, reason string) (string, error) {
	b, err := e.store.GetBenchByProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	next := e.preferLeaderAgent(ctx)
	if next == "" {
		return b.LeaderAgentID, nil
	}
	if next != b.LeaderAgentID {
		if err := e.ReseatLeader(ctx, b.ID, next, reason); err != nil {
			return "", err
		}
	}
	return next, nil
}

func (e *Engine) preferLeaderAgent(ctx context.Context) string {
	order := e.LeadershipOrder()
	if e.registry == nil {
		if len(order) > 0 {
			return order[0]
		}
		return "claude"
	}
	for _, id := range order {
		if e.IsAgentExhausted(id) {
			continue
		}
		if e.leaderAvailable(ctx, id) {
			return id
		}
	}
	// Last resort: first Detect-able even if exhausted, so we still seat someone.
	for _, id := range order {
		if e.leaderAvailable(ctx, id) {
			return id
		}
	}
	if len(order) > 0 {
		return order[0]
	}
	return "claude"
}

func (e *Engine) leaderAvailable(ctx context.Context, id string) bool {
	if id == "" || e.registry == nil {
		return false
	}
	a, ok := e.registry.Get(id)
	if !ok {
		return false
	}
	det, err := a.Detect(ctx)
	if err != nil || !det {
		return false
	}
	entry, found := e.cfg.AgentByID(id)
	if found && !entry.Enabled {
		return false
	}
	return true
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

// ResolveAttentionOpts controls post-resolve behavior (e.g. web resume with agent).
type ResolveAttentionOpts struct {
	// SkipResume prevents OnAttentionResolved (daemon goal auto-resume).
	SkipResume bool
}

// ResolveAttention clears an attention mark.
func (e *Engine) ResolveAttention(ctx context.Context, id string) error {
	_, err := e.ResolveAttentionWith(ctx, id, ResolveAttentionOpts{})
	return err
}

// ResolveAttentionWith clears an attention mark with optional resume control.
func (e *Engine) ResolveAttentionWith(ctx context.Context, id string, opts ResolveAttentionOpts) (storage.Attention, error) {
	a, err := e.store.GetAttention(ctx, id)
	if err != nil {
		return storage.Attention{}, err
	}
	if err := e.store.ResolveAttention(ctx, id, "resolved"); err != nil {
		return storage.Attention{}, err
	}
	_ = e.healBenchAttention(ctx, a.ProjectID, a.BenchID)
	_ = e.emit(ctx, events.KindAttentionResolved, a.RunID, a.TaskID, a.AgentID, "resolved "+id)
	if e.OnAttentionResolved != nil && !opts.SkipResume {
		e.OnAttentionResolved(ctx, a)
	}
	return a, nil
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

// AddBenchWorker seats a worker on an existing bench (idempotent).
func (e *Engine) AddBenchWorker(ctx context.Context, benchID, agentID string) error {
	if benchID == "" || agentID == "" {
		return fmt.Errorf("bench id and agent id required")
	}
	if _, err := e.store.GetBench(ctx, benchID); err != nil {
		return err
	}
	return e.store.UpsertBenchMember(ctx, storage.BenchMember{
		BenchID: benchID, AgentID: agentID, Role: RoleWorker,
	})
}

// RemoveBenchWorker removes a worker from a bench. Refuses to remove the seated leader.
func (e *Engine) RemoveBenchWorker(ctx context.Context, benchID, agentID string) error {
	if benchID == "" || agentID == "" {
		return fmt.Errorf("bench id and agent id required")
	}
	b, err := e.store.GetBench(ctx, benchID)
	if err != nil {
		return err
	}
	if b.LeaderAgentID == agentID {
		return fmt.Errorf("cannot remove the seated leader %q — reseat first", agentID)
	}
	return e.store.DeleteBenchMember(ctx, benchID, agentID)
}

// SetBenchLeader reseats the leader (manual override from UI/CLI).
func (e *Engine) SetBenchLeader(ctx context.Context, benchID, agentID string) error {
	return e.ReseatLeader(ctx, benchID, agentID, "manual seat from workspace")
}
