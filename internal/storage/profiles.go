package storage

import (
	"context"
	"database/sql"
	"time"
)

// AgentProfile is a saved spawn/spec for the web workspace.
type AgentProfile struct {
	ID        string
	Name      string
	AgentID   string
	Role      string
	Traits    string
	Context   string
	ProjectID string
	WorkDir   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Store) UpsertAgentProfile(ctx context.Context, p AgentProfile) error {
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
INSERT INTO agent_profiles(id, name, agent_id, role, traits, context, project_id, work_dir, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, agent_id=excluded.agent_id, role=excluded.role,
  traits=excluded.traits, context=excluded.context, project_id=excluded.project_id,
  work_dir=excluded.work_dir, updated_at=excluded.updated_at
`, p.ID, p.Name, p.AgentID, nullStr(p.Role), nullStr(p.Traits), nullStr(p.Context),
		nullStr(p.ProjectID), nullStr(p.WorkDir),
		p.CreatedAt.UTC().Format(time.RFC3339), p.UpdatedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) GetAgentProfile(ctx context.Context, id string) (AgentProfile, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, agent_id, role, traits, context, project_id, work_dir, created_at, updated_at
FROM agent_profiles WHERE id=?`, id)
	return scanAgentProfile(row)
}

func (s *Store) ListAgentProfiles(ctx context.Context) ([]AgentProfile, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, agent_id, role, traits, context, project_id, work_dir, created_at, updated_at
FROM agent_profiles ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentProfile
	for rows.Next() {
		p, err := scanAgentProfileRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAgentProfile(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_profiles WHERE id=?`, id)
	return err
}

func scanAgentProfile(row *sql.Row) (AgentProfile, error) {
	var p AgentProfile
	var created, updated string
	var role, traits, context, projectID, workDir sql.NullString
	err := row.Scan(&p.ID, &p.Name, &p.AgentID, &role, &traits, &context, &projectID, &workDir, &created, &updated)
	if err != nil {
		return AgentProfile{}, err
	}
	p.Role, p.Traits, p.Context = role.String, traits.String, context.String
	p.ProjectID, p.WorkDir = projectID.String, workDir.String
	p.CreatedAt, _ = time.Parse(time.RFC3339, created)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return p, nil
}

func scanAgentProfileRows(rows *sql.Rows) (AgentProfile, error) {
	var p AgentProfile
	var created, updated string
	var role, traits, context, projectID, workDir sql.NullString
	err := rows.Scan(&p.ID, &p.Name, &p.AgentID, &role, &traits, &context, &projectID, &workDir, &created, &updated)
	if err != nil {
		return AgentProfile{}, err
	}
	p.Role, p.Traits, p.Context = role.String, traits.String, context.String
	p.ProjectID, p.WorkDir = projectID.String, workDir.String
	p.CreatedAt, _ = time.Parse(time.RFC3339, created)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return p, nil
}
