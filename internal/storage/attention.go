package storage

import (
	"context"
	"database/sql"
	"time"
)

// Attention is a mark that the user needs to act (permission, input, approval).
type Attention struct {
	ID        string
	ProjectID string
	BenchID   string
	RunID     string
	TaskID    string
	SessionID string
	AgentID   string
	GoalID    string
	Kind      string // permission | user_input | merge_approval | stalled
	Message   string
	Status    string // open | resolved | dismissed
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Store) UpsertAttention(ctx context.Context, a Attention) error {
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
INSERT INTO attention(id, project_id, bench_id, run_id, task_id, session_id, agent_id, goal_id, kind, message, status, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  status=excluded.status, message=excluded.message, goal_id=excluded.goal_id, updated_at=excluded.updated_at
`, a.ID, nullStr(a.ProjectID), nullStr(a.BenchID), nullStr(a.RunID), nullStr(a.TaskID),
		nullStr(a.SessionID), nullStr(a.AgentID), nullStr(a.GoalID), a.Kind, a.Message, a.Status,
		a.CreatedAt.UTC().Format(time.RFC3339), a.UpdatedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ListOpenAttention(ctx context.Context) ([]Attention, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, bench_id, run_id, task_id, session_id, agent_id, goal_id, kind, message, status, created_at, updated_at
FROM attention WHERE status='open'
ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAttention(rows)
}

func (s *Store) ListAttention(ctx context.Context, limit int) ([]Attention, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, bench_id, run_id, task_id, session_id, agent_id, goal_id, kind, message, status, created_at, updated_at
FROM attention ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAttention(rows)
}

func (s *Store) ResolveAttention(ctx context.Context, id, status string) error {
	if status == "" {
		status = "resolved"
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE attention SET status=?, updated_at=? WHERE id=?`,
		status, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (s *Store) GetAttention(ctx context.Context, id string) (Attention, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, project_id, bench_id, run_id, task_id, session_id, agent_id, goal_id, kind, message, status, created_at, updated_at
FROM attention WHERE id=?`, id)
	var a Attention
	var created, updated string
	var projectID, benchID, runID, taskID, sessionID, agentID, goalID sql.NullString
	err := row.Scan(&a.ID, &projectID, &benchID, &runID, &taskID, &sessionID, &agentID, &goalID,
		&a.Kind, &a.Message, &a.Status, &created, &updated)
	if err != nil {
		return Attention{}, err
	}
	a.ProjectID, a.BenchID, a.RunID = projectID.String, benchID.String, runID.String
	a.TaskID, a.SessionID, a.AgentID = taskID.String, sessionID.String, agentID.String
	a.GoalID = goalID.String
	a.CreatedAt, _ = time.Parse(time.RFC3339, created)
	a.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return a, nil
}

func scanAttention(rows *sql.Rows) ([]Attention, error) {
	var out []Attention
	for rows.Next() {
		var a Attention
		var created, updated string
		var projectID, benchID, runID, taskID, sessionID, agentID, goalID sql.NullString
		if err := rows.Scan(&a.ID, &projectID, &benchID, &runID, &taskID, &sessionID, &agentID, &goalID,
			&a.Kind, &a.Message, &a.Status, &created, &updated); err != nil {
			return nil, err
		}
		a.ProjectID, a.BenchID, a.RunID = projectID.String, benchID.String, runID.String
		a.TaskID, a.SessionID, a.AgentID = taskID.String, sessionID.String, agentID.String
		a.GoalID = goalID.String
		a.CreatedAt, _ = time.Parse(time.RFC3339, created)
		a.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out = append(out, a)
	}
	return out, rows.Err()
}
