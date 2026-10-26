package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Run is an orchestration execution.
type Run struct {
	ID        string
	ProjectID string
	Objective string
	Status    string
	Planner   string
	WorkDir   string
	CreatedAt time.Time
	UpdatedAt time.Time
	EndedAt   *time.Time
	Summary   string
	Error     string
}

// TaskRecord is a persisted orchestrated task.
type TaskRecord struct {
	ID            string
	RunID         string
	ProjectID     string
	Description   string
	Category      string
	Status        string
	AssignedAgent string
	Acceptance    []string
	Dependencies  []string
	MaxRetries    int
	RetryCount    int
	WorktreePath  string
	Branch        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ResultJSON    string
	Error         string
	DiffPath      string
}

func (s *Store) UpsertRun(ctx context.Context, r Run) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	var ended any
	if r.EndedAt != nil {
		ended = r.EndedAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO runs(id, project_id, objective, status, planner, work_dir, created_at, updated_at, ended_at, summary, error)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  status=excluded.status, planner=excluded.planner, updated_at=excluded.updated_at,
  ended_at=excluded.ended_at, summary=excluded.summary, error=excluded.error
`, r.ID, nullStr(r.ProjectID), r.Objective, r.Status, nullStr(r.Planner), nullStr(r.WorkDir),
		r.CreatedAt.UTC().Format(time.RFC3339), now, ended, nullStr(r.Summary), nullStr(r.Error))
	return err
}

func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	var created, updated string
	var ended sql.NullString
	var projectID, planner, workDir, summary, errMsg sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT id, project_id, objective, status, planner, work_dir, created_at, updated_at, ended_at, summary, error
FROM runs WHERE id=?`, id).Scan(
		&r.ID, &projectID, &r.Objective, &r.Status, &planner, &workDir,
		&created, &updated, &ended, &summary, &errMsg,
	)
	if err != nil {
		return Run{}, err
	}
	r.ProjectID = projectID.String
	r.Planner = planner.String
	r.WorkDir = workDir.String
	r.Summary = summary.String
	r.Error = errMsg.String
	r.CreatedAt, _ = time.Parse(time.RFC3339, created)
	r.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	if ended.Valid {
		t, _ := time.Parse(time.RFC3339, ended.String)
		r.EndedAt = &t
	}
	return r, nil
}

func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, COALESCE(project_id,''), objective, status, COALESCE(planner,''), COALESCE(work_dir,''),
       created_at, updated_at, ended_at, COALESCE(summary,''), COALESCE(error,'')
FROM runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var created, updated string
		var ended sql.NullString
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Objective, &r.Status, &r.Planner, &r.WorkDir,
			&created, &updated, &ended, &r.Summary, &r.Error); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, created)
		r.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		if ended.Valid {
			t, _ := time.Parse(time.RFC3339, ended.String)
			r.EndedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpsertTask(ctx context.Context, t TaskRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	acc, _ := json.Marshal(t.Acceptance)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO tasks(id, run_id, project_id, description, category, status, assigned_agent, acceptance_json,
  max_retries, retry_count, worktree_path, branch, created_at, updated_at, result_json, error, diff_path)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  status=excluded.status, assigned_agent=excluded.assigned_agent, retry_count=excluded.retry_count,
  worktree_path=excluded.worktree_path, branch=excluded.branch, updated_at=excluded.updated_at,
  result_json=excluded.result_json, error=excluded.error, diff_path=excluded.diff_path,
  category=excluded.category, description=excluded.description
`, t.ID, nullStr(t.RunID), nullStr(t.ProjectID), t.Description, nullStr(t.Category), t.Status,
		nullStr(t.AssignedAgent), string(acc), t.MaxRetries, t.RetryCount,
		nullStr(t.WorktreePath), nullStr(t.Branch),
		t.CreatedAt.UTC().Format(time.RFC3339), now,
		nullStr(t.ResultJSON), nullStr(t.Error), nullStr(t.DiffPath))
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM task_dependencies WHERE task_id=?`, t.ID); err != nil {
		return err
	}
	for _, dep := range t.Dependencies {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO task_dependencies(task_id, depends_on) VALUES(?,?)`, t.ID, dep); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListTasksByRun(ctx context.Context, runID string) ([]TaskRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, COALESCE(run_id,''), COALESCE(project_id,''), description, COALESCE(category,''), status,
       COALESCE(assigned_agent,''), COALESCE(acceptance_json,'[]'), max_retries, retry_count,
       COALESCE(worktree_path,''), COALESCE(branch,''), created_at, updated_at,
       COALESCE(result_json,''), COALESCE(error,''), COALESCE(diff_path,'')
FROM tasks WHERE run_id=? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRecord
	for rows.Next() {
		var t TaskRecord
		var created, updated, acc string
		if err := rows.Scan(&t.ID, &t.RunID, &t.ProjectID, &t.Description, &t.Category, &t.Status,
			&t.AssignedAgent, &acc, &t.MaxRetries, &t.RetryCount, &t.WorktreePath, &t.Branch,
			&created, &updated, &t.ResultJSON, &t.Error, &t.DiffPath); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(acc), &t.Acceptance)
		t.CreatedAt, _ = time.Parse(time.RFC3339, created)
		t.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		deps, err := s.taskDeps(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		t.Dependencies = deps
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTask(ctx context.Context, id string) (TaskRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, COALESCE(run_id,''), COALESCE(project_id,''), description, COALESCE(category,''), status,
       COALESCE(assigned_agent,''), COALESCE(acceptance_json,'[]'), max_retries, retry_count,
       COALESCE(worktree_path,''), COALESCE(branch,''), created_at, updated_at,
       COALESCE(result_json,''), COALESCE(error,''), COALESCE(diff_path,'')
FROM tasks WHERE id=?`, id)
	if err != nil {
		return TaskRecord{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return TaskRecord{}, fmt.Errorf("task %q not found", id)
	}
	var t TaskRecord
	var created, updated, acc string
	if err := rows.Scan(&t.ID, &t.RunID, &t.ProjectID, &t.Description, &t.Category, &t.Status,
		&t.AssignedAgent, &acc, &t.MaxRetries, &t.RetryCount, &t.WorktreePath, &t.Branch,
		&created, &updated, &t.ResultJSON, &t.Error, &t.DiffPath); err != nil {
		return TaskRecord{}, err
	}
	_ = json.Unmarshal([]byte(acc), &t.Acceptance)
	t.CreatedAt, _ = time.Parse(time.RFC3339, created)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	deps, err := s.taskDeps(ctx, t.ID)
	if err != nil {
		return TaskRecord{}, err
	}
	t.Dependencies = deps
	return t, nil
}

func (s *Store) taskDeps(ctx context.Context, taskID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT depends_on FROM task_dependencies WHERE task_id=?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deps []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}
