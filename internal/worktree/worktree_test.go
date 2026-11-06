package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestDiffExcludesUserStateAndIncludesAgentCommits(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "git", "init")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test")
	mustWrite(t, filepath.Join(repo, "README.md"), "initial\n")
	mustWrite(t, filepath.Join(repo, "deleted.txt"), "remove me\n")
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-m", "init")
	mustWrite(t, filepath.Join(repo, "README.md"), "user edit\n")
	if err := os.Remove(filepath.Join(repo, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(repo, "user.txt"), "untracked user file\n")
	mgr := New(repo, filepath.Join(repo, ".qma-worktrees"))
	dir, err := mgr.Create(context.Background(), "snapshot", "qma/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "deleted.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file resurrected: %v", err)
	}
	diff, err := mgr.Diff(context.Background(), dir)
	if err != nil || diff != "" {
		t.Fatalf("initial diff=%q err=%v", diff, err)
	}
	mustWrite(t, filepath.Join(dir, "agent.txt"), "agent change\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "agent commit")
	diff, err = mgr.Diff(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+agent change") || strings.Contains(diff, "user edit") || strings.Contains(diff, "untracked user file") {
		t.Fatalf("wrong diff: %s", diff)
	}
}
