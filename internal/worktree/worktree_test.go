package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateSyncsUntrackedFiles(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "git", "init")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test")
	mustWrite(t, filepath.Join(repo, "LICENSE"), "mit\n")
	run(t, repo, "git", "add", "LICENSE")
	run(t, repo, "git", "commit", "-m", "init")

	// Untracked project files — the failure mode we hit in production.
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(repo, "README.md"), "# example\n")
	mustWrite(t, filepath.Join(repo, "internal", "x.go"), "package internal\n")

	base := filepath.Join(repo, ".qma-worktrees")
	mgr := New(repo, base)
	dir, err := mgr.Create(context.Background(), "task-1", "qma/test-task-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "README.md", "internal/x.go"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing synced file %s: %v", name, err)
		}
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v (%s)", name, args, err, out)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
