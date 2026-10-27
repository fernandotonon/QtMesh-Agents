package storage

import (
	"context"
	"database/sql"
	"time"
)

// ReviewRecord is a persisted static (or agent) review of a task/run diff.
type ReviewRecord struct {
	ID          string
	RunID       string
	TaskID      string
	ProjectID   string
	Summary     string
	Approved    bool
	Findings    string // JSON
	DiffSnippet string
	CreatedAt   time.Time
}

func (s *Store) UpsertReview(ctx context.Context, r ReviewRecord) error {
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.ID == "" {
		r.ID = "rev-" + now.Format("20060102150405.000000000")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO reviews(id, run_id, task_id, project_id, summary, approved, findings_json, diff_snippet, created_at)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  summary=excluded.summary, approved=excluded.approved, findings_json=excluded.findings_json,
  diff_snippet=excluded.diff_snippet
`, r.ID, nullStr(r.RunID), nullStr(r.TaskID), nullStr(r.ProjectID), r.Summary,
		boolInt(r.Approved), nullStr(r.Findings), nullStr(r.DiffSnippet),
		r.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ListReviewsByRun(ctx context.Context, runID string) ([]ReviewRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, task_id, project_id, summary, approved, findings_json, diff_snippet, created_at
FROM reviews WHERE run_id=? ORDER BY created_at ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReviews(rows)
}

func (s *Store) ListRecentReviews(ctx context.Context, limit int) ([]ReviewRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, task_id, project_id, summary, approved, findings_json, diff_snippet, created_at
FROM reviews ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReviews(rows)
}

func (s *Store) GetLatestReviewForRun(ctx context.Context, runID string) (ReviewRecord, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, run_id, task_id, project_id, summary, approved, findings_json, diff_snippet, created_at
FROM reviews WHERE run_id=? ORDER BY created_at DESC LIMIT 1`, runID)
	return scanReviewRow(row)
}

func scanReviews(rows *sql.Rows) ([]ReviewRecord, error) {
	var out []ReviewRecord
	for rows.Next() {
		r, err := scanReviewCols(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanReviewRow(row rowScanner) (ReviewRecord, error) {
	return scanReviewCols(row)
}

func scanReviewCols(row rowScanner) (ReviewRecord, error) {
	var r ReviewRecord
	var runID, taskID, projectID, findings, snippet sql.NullString
	var approved int
	var created string
	if err := row.Scan(&r.ID, &runID, &taskID, &projectID, &r.Summary, &approved, &findings, &snippet, &created); err != nil {
		return r, err
	}
	r.RunID = runID.String
	r.TaskID = taskID.String
	r.ProjectID = projectID.String
	r.Approved = approved != 0
	r.Findings = findings.String
	r.DiffSnippet = snippet.String
	r.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return r, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
