package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/scheduler"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

// artifactFrontier selects cumulative workspaces, dropping ancestors already
// included by a later workspace. Non-workspace tasks (such as discovery) still
// forward their ancestors' artifacts.
func artifactFrontier(records []storage.TaskRecord) []storage.TaskRecord {
	byID := make(map[string]storage.TaskRecord, len(records))
	for _, r := range records {
		byID[r.ID] = r
	}
	covered := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if covered[id] {
			return
		}
		covered[id] = true
		for _, dep := range byID[id].Dependencies {
			visit(dep)
		}
	}
	for _, r := range records {
		if r.WorktreePath != "" || r.DiffPath != "" {
			for _, dep := range r.Dependencies {
				visit(dep)
			}
		}
	}
	var selected []storage.TaskRecord
	for _, r := range records {
		if !covered[r.ID] && (r.WorktreePath != "" || r.DiffPath != "") {
			selected = append(selected, r)
		}
	}
	return selected
}

func (e *Executor) prepareDependencies(ctx context.Context, runID string, task *scheduler.Task, dir string) error {
	if e.Store == nil || len(task.Dependencies) == 0 {
		return nil
	}
	records, err := e.Store.ListTasksByRun(ctx, runID)
	if err != nil {
		return err
	}
	byID := make(map[string]storage.TaskRecord, len(records))
	for _, r := range records {
		byID[r.ID] = r
	}
	needed := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if needed[id] {
			return nil
		}
		r, ok := byID[id]
		if !ok {
			return fmt.Errorf("missing dependency %s", id)
		}
		if r.Status != string(scheduler.StatusSucceeded) {
			return fmt.Errorf("dependency %s is %s", id, r.Status)
		}
		needed[id] = true
		for _, dep := range r.Dependencies {
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for _, dep := range task.Dependencies {
		if err := visit(dep); err != nil {
			return err
		}
	}
	var ancestors []storage.TaskRecord
	for _, r := range records {
		if needed[r.ID] {
			ancestors = append(ancestors, r)
		}
	}
	var patches []string
	for _, r := range artifactFrontier(ancestors) {
		// Use the immutable captured artifact, not a workspace that can keep changing.
		if r.DiffPath == "" {
			return fmt.Errorf("dependency %s has no captured diff", r.ID)
		}
		b, err := os.ReadFile(r.DiffPath)
		if err != nil {
			return fmt.Errorf("dependency %s: %w", r.ID, err)
		}
		if strings.TrimSpace(string(b)) != "" {
			patches = append(patches, string(b))
		}
	}
	if len(patches) > 0 {
		if err := applyUnifiedDiff(dir, strings.Join(patches, "\n")); err != nil {
			return fmt.Errorf("prepare dependency workspace: %w", err)
		}
	}
	return nil
}
