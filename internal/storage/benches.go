package storage

import (
	"context"
	"database/sql"
	"time"
)

// Bench is a per-project working team managed by the orchestrator.
type Bench struct {
	ID            string
	ProjectID     string
	Name          string
	LeaderAgentID string
	Status        string // idle | active | needs_attention | closed
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// BenchMember is an agent assigned to a bench with a role.
type BenchMember struct {
	BenchID   string
	AgentID   string
	Role      string // leader | worker
	SessionID string
	TaskID    string
}

func (s *Store) UpsertBench(ctx context.Context, b Bench) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO benches(id, project_id, name, leader_agent_id, status, created_at, updated_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, leader_agent_id=excluded.leader_agent_id,
  status=excluded.status, updated_at=excluded.updated_at
`, b.ID, b.ProjectID, b.Name, nullStr(b.LeaderAgentID), b.Status,
		b.CreatedAt.UTC().Format(time.RFC3339), now)
	return err
}

func (s *Store) GetBench(ctx context.Context, id string) (Bench, error) {
	var b Bench
	var created, updated string
	var leader sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT id, project_id, name, leader_agent_id, status, created_at, updated_at
FROM benches WHERE id=?`, id).Scan(
		&b.ID, &b.ProjectID, &b.Name, &leader, &b.Status, &created, &updated,
	)
	if err != nil {
		return Bench{}, err
	}
	b.LeaderAgentID = leader.String
	b.CreatedAt, _ = time.Parse(time.RFC3339, created)
	b.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return b, nil
}

func (s *Store) GetBenchByProject(ctx context.Context, projectID string) (Bench, error) {
	var b Bench
	var created, updated string
	var leader sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT id, project_id, name, leader_agent_id, status, created_at, updated_at
FROM benches WHERE project_id=? AND status != 'closed'
ORDER BY updated_at DESC LIMIT 1`, projectID).Scan(
		&b.ID, &b.ProjectID, &b.Name, &leader, &b.Status, &created, &updated,
	)
	if err != nil {
		return Bench{}, err
	}
	b.LeaderAgentID = leader.String
	b.CreatedAt, _ = time.Parse(time.RFC3339, created)
	b.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return b, nil
}

func (s *Store) ListBenches(ctx context.Context) ([]Bench, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, name, leader_agent_id, status, created_at, updated_at
FROM benches WHERE status != 'closed'
ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bench
	for rows.Next() {
		var b Bench
		var created, updated string
		var leader sql.NullString
		if err := rows.Scan(&b.ID, &b.ProjectID, &b.Name, &leader, &b.Status, &created, &updated); err != nil {
			return nil, err
		}
		b.LeaderAgentID = leader.String
		b.CreatedAt, _ = time.Parse(time.RFC3339, created)
		b.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) UpsertBenchMember(ctx context.Context, m BenchMember) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bench_members(bench_id, agent_id, role, session_id, task_id)
VALUES(?,?,?,?,?)
ON CONFLICT(bench_id, agent_id) DO UPDATE SET
  role=excluded.role, session_id=excluded.session_id, task_id=excluded.task_id
`, m.BenchID, m.AgentID, m.Role, nullStr(m.SessionID), nullStr(m.TaskID))
	return err
}

func (s *Store) ListBenchMembers(ctx context.Context, benchID string) ([]BenchMember, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT bench_id, agent_id, role, COALESCE(session_id,''), COALESCE(task_id,'')
FROM bench_members WHERE bench_id=?`, benchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BenchMember
	for rows.Next() {
		var m BenchMember
		if err := rows.Scan(&m.BenchID, &m.AgentID, &m.Role, &m.SessionID, &m.TaskID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBenchMember(ctx context.Context, benchID, agentID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM bench_members WHERE bench_id=? AND agent_id=?`, benchID, agentID)
	return err
}

func (s *Store) SetRunBenchID(ctx context.Context, runID, benchID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET bench_id=?, updated_at=? WHERE id=?`,
		nullStr(benchID), time.Now().UTC().Format(time.RFC3339), runID)
	return err
}

func (s *Store) GetRunBenchID(ctx context.Context, runID string) (string, error) {
	var id sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT bench_id FROM runs WHERE id=?`, runID).Scan(&id)
	if err != nil {
		return "", err
	}
	return id.String, nil
}
