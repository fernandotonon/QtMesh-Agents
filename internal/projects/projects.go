package projects

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

// Service manages registered projects.
type Service struct {
	store *storage.Store
}

func New(store *storage.Store) *Service {
	return &Service{store: store}
}

func (s *Service) Add(ctx context.Context, path, name string) (storage.Project, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return storage.Project{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return storage.Project{}, err
	}
	if !st.IsDir() {
		return storage.Project{}, fmt.Errorf("%s is not a directory", abs)
	}
	if name == "" {
		name = filepath.Base(abs)
	}
	id := slug(name)
	p := storage.Project{ID: id, Name: name, Path: abs, Active: false}
	if err := s.store.UpsertProject(ctx, p); err != nil {
		return storage.Project{}, err
	}
	// No auto-active project: the orchestrator works across all registered repos.
	return p, nil
}

func (s *Service) List(ctx context.Context) ([]storage.Project, error) {
	return s.store.ListProjects(ctx)
}

func (s *Service) Use(ctx context.Context, idOrName string) error {
	list, err := s.store.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, p := range list {
		if p.ID == idOrName || strings.EqualFold(p.Name, idOrName) {
			return s.store.SetActiveProject(ctx, p.ID)
		}
	}
	return fmt.Errorf("project %q not found", idOrName)
}

func (s *Service) Active(ctx context.Context) (storage.Project, error) {
	return s.store.ActiveProject(ctx)
}

func slug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == ' ' || r == '_' || r == '-' {
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "project"
	}
	return out
}
