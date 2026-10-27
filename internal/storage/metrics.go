package storage

import (
	"context"
	"database/sql"
	"time"
)

// RunMetric is a lightweight historical record for a finished run.
type RunMetric struct {
	ID         string
	RunID      string
	ProjectID  string
	AgentID    string
	DurationMS int64
	Retries    int
	Success    bool
	CreatedAt  time.Time
}

func (s *Store) UpsertRunMetric(ctx context.Context, m RunMetric) error {
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.ID == "" {
		m.ID = "met-" + m.RunID
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO run_metrics(id, run_id, project_id, agent_id, duration_ms, retries, success, created_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  duration_ms=excluded.duration_ms, retries=excluded.retries, success=excluded.success,
  agent_id=excluded.agent_id
`, m.ID, nullStr(m.RunID), nullStr(m.ProjectID), nullStr(m.AgentID),
		m.DurationMS, m.Retries, boolInt(m.Success), m.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ListRunMetrics(ctx context.Context, limit int) ([]RunMetric, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, project_id, agent_id, duration_ms, retries, success, created_at
FROM run_metrics ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunMetric
	for rows.Next() {
		var m RunMetric
		var runID, projectID, agentID sql.NullString
		var success int
		var created string
		if err := rows.Scan(&m.ID, &runID, &projectID, &agentID, &m.DurationMS, &m.Retries, &success, &created); err != nil {
			return nil, err
		}
		m.RunID = runID.String
		m.ProjectID = projectID.String
		m.AgentID = agentID.String
		m.Success = success != 0
		m.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, m)
	}
	return out, rows.Err()
}
