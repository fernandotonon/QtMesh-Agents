package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Thread roles for bench work threads.
const (
	ThreadRoleUser      = "user"
	ThreadRoleLeader    = "leader"
	ThreadRoleWorker    = "worker"
	ThreadRoleSystem    = "system"
	ThreadRoleAttention = "attention"
)

// Goal statuses for durable bench goals.
const (
	GoalActive          = "active"
	GoalPausedNeedsYou  = "paused_needs_you"
	GoalSucceeded       = "succeeded"
	GoalFailed          = "failed"
	GoalCancelled       = "cancelled"
)

// BenchThread is the durable conversation for a project bench.
type BenchThread struct {
	ID        string
	BenchID   string
	ProjectID string
	CreatedAt time.Time
}

// ThreadMessage is one append-only entry on a bench thread.
type ThreadMessage struct {
	ID        string
	ThreadID  string
	Role      string
	AgentID   string
	Content   string
	CreatedAt time.Time
}

// BenchGoal is a durable objective the bench loop works until done or Needs you.
type BenchGoal struct {
	ID         string
	BenchID    string
	ProjectID  string
	ThreadID   string
	Objective  string
	Status     string
	Iteration  int
	RunID      string
	Error      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (s *Store) UpsertBenchThread(ctx context.Context, t BenchThread) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bench_threads(id, bench_id, project_id, created_at)
VALUES(?,?,?,?)
ON CONFLICT(id) DO NOTHING
`, t.ID, t.BenchID, t.ProjectID, t.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) GetBenchThread(ctx context.Context, id string) (BenchThread, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, bench_id, project_id, created_at FROM bench_threads WHERE id=?`, id)
	var t BenchThread
	var created string
	if err := row.Scan(&t.ID, &t.BenchID, &t.ProjectID, &created); err != nil {
		return BenchThread{}, err
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return t, nil
}

func (s *Store) GetBenchThreadByBench(ctx context.Context, benchID string) (BenchThread, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, bench_id, project_id, created_at FROM bench_threads
WHERE bench_id=? ORDER BY created_at DESC LIMIT 1`, benchID)
	var t BenchThread
	var created string
	if err := row.Scan(&t.ID, &t.BenchID, &t.ProjectID, &created); err != nil {
		return BenchThread{}, err
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return t, nil
}

func (s *Store) AppendThreadMessage(ctx context.Context, m ThreadMessage) error {
	if m.ID == "" {
		return fmt.Errorf("thread message id required")
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bench_thread_messages(id, thread_id, role, agent_id, content, created_at)
VALUES(?,?,?,?,?,?)
`, m.ID, m.ThreadID, m.Role, nullStr(m.AgentID), m.Content, m.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ListThreadMessages(ctx context.Context, threadID string, limit int) ([]ThreadMessage, error) {
	if limit <= 0 {
		limit = 40
	}
	// Newest-last for callers: fetch last N then reverse.
	rows, err := s.db.QueryContext(ctx, `
SELECT id, thread_id, role, agent_id, content, created_at
FROM (
  SELECT id, thread_id, role, agent_id, content, created_at
  FROM bench_thread_messages WHERE thread_id=?
  ORDER BY created_at DESC LIMIT ?
) AS recent ORDER BY created_at ASC`, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadMessage
	for rows.Next() {
		var m ThreadMessage
		var created string
		var agent sql.NullString
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &agent, &m.Content, &created); err != nil {
			return nil, err
		}
		m.AgentID = agent.String
		m.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) UpsertBenchGoal(ctx context.Context, g BenchGoal) error {
	now := time.Now().UTC()
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now
	}
	g.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bench_goals(id, bench_id, project_id, thread_id, objective, status, iteration, run_id, error, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  status=excluded.status, iteration=excluded.iteration, run_id=excluded.run_id,
  error=excluded.error, updated_at=excluded.updated_at, objective=excluded.objective
`, g.ID, g.BenchID, g.ProjectID, g.ThreadID, g.Objective, g.Status, g.Iteration,
		nullStr(g.RunID), nullStr(g.Error),
		g.CreatedAt.UTC().Format(time.RFC3339), g.UpdatedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) GetBenchGoal(ctx context.Context, id string) (BenchGoal, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, bench_id, project_id, thread_id, objective, status, iteration, run_id, error, created_at, updated_at
FROM bench_goals WHERE id=?`, id)
	return scanBenchGoal(row)
}

func (s *Store) GetActiveBenchGoal(ctx context.Context, projectID string) (BenchGoal, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, bench_id, project_id, thread_id, objective, status, iteration, run_id, error, created_at, updated_at
FROM bench_goals
WHERE project_id=? AND status IN ('active','paused_needs_you')
ORDER BY updated_at DESC LIMIT 1`, projectID)
	return scanBenchGoal(row)
}

func (s *Store) LatestBenchGoal(ctx context.Context, projectID string) (BenchGoal, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, bench_id, project_id, thread_id, objective, status, iteration, run_id, error, created_at, updated_at
FROM bench_goals WHERE project_id=?
ORDER BY updated_at DESC LIMIT 1`, projectID)
	return scanBenchGoal(row)
}

func (s *Store) ListBenchGoals(ctx context.Context, projectID string, limit int) ([]BenchGoal, error) {
	if limit <= 0 {
		limit = 10
	}
	q := `
SELECT id, bench_id, project_id, thread_id, objective, status, iteration, run_id, error, created_at, updated_at
FROM bench_goals`
	args := []any{}
	if projectID != "" {
		q += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	q += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BenchGoal
	for rows.Next() {
		g, err := scanBenchGoalRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func scanBenchGoal(row *sql.Row) (BenchGoal, error) {
	var g BenchGoal
	var created, updated string
	var runID, errMsg sql.NullString
	err := row.Scan(&g.ID, &g.BenchID, &g.ProjectID, &g.ThreadID, &g.Objective, &g.Status,
		&g.Iteration, &runID, &errMsg, &created, &updated)
	if err != nil {
		return BenchGoal{}, err
	}
	g.RunID, g.Error = runID.String, errMsg.String
	g.CreatedAt, _ = time.Parse(time.RFC3339, created)
	g.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return g, nil
}

func scanBenchGoalRows(rows *sql.Rows) (BenchGoal, error) {
	var g BenchGoal
	var created, updated string
	var runID, errMsg sql.NullString
	err := rows.Scan(&g.ID, &g.BenchID, &g.ProjectID, &g.ThreadID, &g.Objective, &g.Status,
		&g.Iteration, &runID, &errMsg, &created, &updated)
	if err != nil {
		return BenchGoal{}, err
	}
	g.RunID, g.Error = runID.String, errMsg.String
	g.CreatedAt, _ = time.Parse(time.RFC3339, created)
	g.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return g, nil
}
