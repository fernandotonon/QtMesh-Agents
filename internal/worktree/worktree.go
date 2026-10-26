package worktree

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Manager creates and tracks Git worktrees for isolated task execution.
type Manager struct {
	RepoRoot string
	BaseDir  string
}

func New(repoRoot, baseDir string) *Manager {
	return &Manager{RepoRoot: repoRoot, BaseDir: baseDir}
}

// Create makes a new branch + worktree for a task, then syncs the current
// working tree (including untracked files) into it so agents see local work.
func (m *Manager) Create(ctx context.Context, taskID, branch string) (string, error) {
	if branch == "" {
		branch = "qma/" + sanitize(taskID)
	}
	dir := filepath.Join(m.BaseDir, sanitize(taskID))
	if err := os.MkdirAll(m.BaseDir, 0o700); err != nil {
		return "", err
	}
	// Create branch from HEAD without checking it out in the main worktree.
	cmd := exec.CommandContext(ctx, "git", "-C", m.RepoRoot, "worktree", "add", "-b", branch, dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if err := SyncWorkingTree(m.RepoRoot, dir); err != nil {
		_ = m.Remove(ctx, dir)
		return "", fmt.Errorf("sync working tree into worktree: %w", err)
	}
	if err := ValidateWorktree(m.RepoRoot, dir); err != nil {
		_ = m.Remove(ctx, dir)
		return "", err
	}
	return dir, nil
}

// SyncWorkingTree copies the main working tree into dest, preserving the
// destination's .git worktree link. This includes untracked and uncommitted files.
func SyncWorkingTree(src, dest string) error {
	if _, err := exec.LookPath("rsync"); err == nil {
		args := []string{
			"-a",
			"--exclude", ".git",
			"--exclude", ".qma-worktrees",
			"--exclude", "bin",
			"--exclude", "dist",
			"--exclude", ".DS_Store",
			filepath.Clean(src) + "/",
			filepath.Clean(dest) + "/",
		}
		cmd := exec.Command("rsync", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("rsync: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return copyTree(src, dest)
}

func copyTree(src, dest string) error {
	skip := map[string]bool{
		".git": true, ".qma-worktrees": true, "bin": true, "dist": true, ".DS_Store": true,
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(path)
		if skip[base] || skip[rel] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// ValidateWorktree fails fast when the worktree is missing key files that exist in the source repo.
func ValidateWorktree(src, dest string) error {
	checks := []string{"go.mod", "README.md", "cmd", "internal", "package.json", "Cargo.toml", "pyproject.toml"}
	var missing []string
	for _, name := range checks {
		srcPath := filepath.Join(src, name)
		if _, err := os.Stat(srcPath); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("worktree %s is missing %v (are project files uncommitted? sync failed)", dest, missing)
	}
	return nil
}

// Diff returns a unified diff of all changes in the worktree versus HEAD,
// including previously untracked files.
func (m *Manager) Diff(ctx context.Context, worktreePath string) (string, error) {
	// Stage everything so untracked files appear in the diff without committing.
	add := exec.CommandContext(ctx, "git", "-C", worktreePath, "add", "-A")
	if out, err := add.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "diff", "--cached", "HEAD")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Remove removes a worktree (does not delete the branch).
func (m *Manager) Remove(ctx context.Context, worktreePath string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", m.RepoRoot, "worktree", "remove", "--force", worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
