package storage

import (
	"fmt"
	"time"
)

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  project_id TEXT,
  work_dir TEXT,
  status TEXT NOT NULL,
  pid INTEGER,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  ended_at TEXT,
  log_path TEXT,
  resume_id TEXT,
  exit_code INTEGER
);

CREATE TABLE IF NOT EXISTS events (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  timestamp TEXT NOT NULL,
  session_id TEXT,
  project_id TEXT,
  agent_id TEXT,
  task_id TEXT,
  message TEXT,
  data_json TEXT
);

CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  project_id TEXT,
  objective TEXT NOT NULL,
  status TEXT NOT NULL,
  planner TEXT,
  work_dir TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  ended_at TEXT,
  summary TEXT,
  error TEXT
);

CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  run_id TEXT,
  project_id TEXT,
  description TEXT NOT NULL,
  category TEXT,
  status TEXT NOT NULL,
  assigned_agent TEXT,
  acceptance_json TEXT,
  max_retries INTEGER NOT NULL DEFAULT 2,
  retry_count INTEGER NOT NULL DEFAULT 0,
  worktree_path TEXT,
  branch TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  result_json TEXT,
  error TEXT,
  diff_path TEXT
);

CREATE TABLE IF NOT EXISTS task_dependencies (
  task_id TEXT NOT NULL,
  depends_on TEXT NOT NULL,
  PRIMARY KEY (task_id, depends_on)
);
`)
	if err != nil {
		return err
	}
	if err := s.ensureMigration(1); err != nil {
		return err
	}
	// Migration 2: ensure run_id / category columns exist on older DBs.
	if err := s.ensureColumns(); err != nil {
		return err
	}
	return s.ensureMigration(2)
}

func (s *Store) ensureMigration(version int) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
		version, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ensureColumns() error {
	cols := []struct {
		table, column, decl string
	}{
		{"tasks", "run_id", "TEXT"},
		{"tasks", "category", "TEXT"},
		{"tasks", "error", "TEXT"},
		{"tasks", "diff_path", "TEXT"},
	}
	for _, c := range cols {
		if s.columnExists(c.table, c.column) {
			continue
		}
		_, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, c.table, c.column, c.decl))
		if err != nil {
			return fmt.Errorf("add column %s.%s: %w", c.table, c.column, err)
		}
	}
	return nil
}

func (s *Store) columnExists(table, column string) bool {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}
