package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the SQLite persistence layer.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and runs migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Reasonable defaults for local daemon use.
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the underlying handle for advanced queries (tests).
func (s *Store) DB() *sql.DB { return s.db }

// --- Projects ---

type Project struct {
	ID        string
	Name      string
	Path      string
	CreatedAt time.Time
	UpdatedAt time.Time
	Active    bool
}

func (s *Store) UpsertProject(ctx context.Context, p Project) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	active := 0
	if p.Active {
		active = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO projects(id, name, path, created_at, updated_at, active)
VALUES(?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, path=excluded.path, updated_at=excluded.updated_at, active=excluded.active
`, p.ID, p.Name, p.Path, p.CreatedAt.UTC().Format(time.RFC3339), now, active)
	return err
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, path, created_at, updated_at, active FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var created, updated string
		var active int
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &created, &updated, &active); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, created)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		p.Active = active == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SetActiveProject(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET active=0`); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE projects SET active=1, updated_at=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("project %q not found", id)
	}
	return tx.Commit()
}

func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	var p Project
	var created, updated string
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT id, name, path, created_at, updated_at, active FROM projects WHERE id=?`, id).
		Scan(&p.ID, &p.Name, &p.Path, &created, &updated, &active)
	if err != nil {
		return Project{}, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, created)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	p.Active = active == 1
	return p, nil
}

func (s *Store) ActiveProject(ctx context.Context) (Project, error) {
	var p Project
	var created, updated string
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT id, name, path, created_at, updated_at, active FROM projects WHERE active=1 LIMIT 1`).
		Scan(&p.ID, &p.Name, &p.Path, &created, &updated, &active)
	if err != nil {
		return Project{}, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, created)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	p.Active = true
	return p, nil
}

// --- Sessions ---

type SessionRecord struct {
	ID        string
	AgentID   string
	ProjectID string
	WorkDir   string
	Status    string
	PID       int
	CreatedAt time.Time
	UpdatedAt time.Time
	EndedAt   *time.Time
	LogPath   string
	ResumeID  string
	ExitCode  *int
}

func (s *Store) UpsertSession(ctx context.Context, rec SessionRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	var ended any
	if rec.EndedAt != nil {
		ended = rec.EndedAt.UTC().Format(time.RFC3339)
	}
	var exit any
	if rec.ExitCode != nil {
		exit = *rec.ExitCode
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions(id, agent_id, project_id, work_dir, status, pid, created_at, updated_at, ended_at, log_path, resume_id, exit_code)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  status=excluded.status, pid=excluded.pid, updated_at=excluded.updated_at,
  ended_at=excluded.ended_at, log_path=excluded.log_path, resume_id=excluded.resume_id, exit_code=excluded.exit_code
`, rec.ID, rec.AgentID, nullStr(rec.ProjectID), nullStr(rec.WorkDir), rec.Status, rec.PID,
		rec.CreatedAt.UTC().Format(time.RFC3339), now, ended, nullStr(rec.LogPath), nullStr(rec.ResumeID), exit)
	return err
}

func (s *Store) ListSessions(ctx context.Context) ([]SessionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, agent_id, COALESCE(project_id,''), COALESCE(work_dir,''), status, COALESCE(pid,0),
       created_at, updated_at, ended_at, COALESCE(log_path,''), COALESCE(resume_id,''), exit_code
FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRecord
	for rows.Next() {
		var r SessionRecord
		var created, updated string
		var ended sql.NullString
		var exit sql.NullInt64
		if err := rows.Scan(&r.ID, &r.AgentID, &r.ProjectID, &r.WorkDir, &r.Status, &r.PID,
			&created, &updated, &ended, &r.LogPath, &r.ResumeID, &exit); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, created)
		r.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		if ended.Valid {
			t, _ := time.Parse(time.RFC3339, ended.String)
			r.EndedAt = &t
		}
		if exit.Valid {
			v := int(exit.Int64)
			r.ExitCode = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetSession(ctx context.Context, id string) (SessionRecord, error) {
	list, err := s.ListSessions(ctx)
	if err != nil {
		return SessionRecord{}, err
	}
	for _, r := range list {
		if r.ID == id {
			return r, nil
		}
	}
	return SessionRecord{}, fmt.Errorf("session %q not found", id)
}

// --- Events ---

func (s *Store) InsertEvent(ctx context.Context, id, kind, ts, sessionID, projectID, agentID, taskID, message, dataJSON string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO events(id, kind, timestamp, session_id, project_id, agent_id, task_id, message, data_json)
VALUES(?,?,?,?,?,?,?,?,?)`, id, kind, ts, nullStr(sessionID), nullStr(projectID), nullStr(agentID), nullStr(taskID), message, nullStr(dataJSON))
	return err
}

func (s *Store) ListEvents(ctx context.Context, limit int) ([]map[string]string, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, timestamp, COALESCE(session_id,''), COALESCE(agent_id,''), COALESCE(message,'')
FROM events ORDER BY timestamp DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var id, kind, ts, sid, aid, msg string
		if err := rows.Scan(&id, &kind, &ts, &sid, &aid, &msg); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{
			"id": id, "kind": kind, "timestamp": ts, "session_id": sid, "agent_id": aid, "message": msg,
		})
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
